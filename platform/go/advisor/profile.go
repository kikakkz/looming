// SPDX-License-Identifier: Apache-2.0

// Package advisor is the management agent's deterministic hard layer
// (advisor-l1, AD-38): the ComponentProfile schema and loader, and the
// pure feasibility evaluator that matches declared host facts against
// profile hard rules. Everything here is a pure function of its inputs
// — no I/O, no model access, no global state, the same discipline as
// the rest of platform/go (AD-38 decision 7: domain logic lives in the
// platform kit, the CLI owns the interaction).
package advisor

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kikakkz/looming/platform/go/config"
	"github.com/kikakkz/looming/platform/go/render"
)

// Profile errors. Loading is fail-closed (advisor-l1 §2): a schema
// violation, a name outside the render allowlist, or a dangling kind
// reference is a load error naming the offender, never a silent
// default.
var (
	ErrInvalidProfiles    = errors.New("advisor: invalid profiles")
	ErrUnknownProfileName = errors.New("advisor: profile name outside the render allowlist")
	ErrUnknownKind        = errors.New("advisor: profile references an unknown kind")
)

// SpreadComponent is the only soft.spread vocabulary value (advisor-l1
// §2): instances of the component prefer distinct hosts.
const SpreadComponent = "component"

// portNamePattern constrains claimed port names to the same vocabulary
// config's placement ports use — keep in sync with
// config.portNamePattern.
var portNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Profile is one component's requirement profile: review-gated
// knowledge shipped as data (AD-38 decision 3). Name must match the
// render allowlist vocabulary — the loader fails closed otherwise.
// Hard feeds the evaluator only; Soft serializes verbatim into the
// model context (slice 1.2) and carries no feasibility semantics here.
type Profile struct {
	Name string
	Kind string
	Hard Hard
	Soft Soft
}

// Hard is the evaluator's input: the floors and constraints a host's
// declared facts must satisfy. Zero/empty fields declare no
// constraint — the fact-completeness rule fires only for inputs a
// non-zero constraint actually needs.
type Hard struct {
	MinCPUCores int
	MinMemoryMB int
	MinDiskGB   int
	Arch        []string
	NeedsEgress bool
	Ports       []string
}

// Soft is the slice-1.2 model context: placement preferences the
// deterministic layer deliberately does not judge.
type Soft struct {
	PreferredZone string
	Spread        string
}

// rawProfileSet mirrors the YAML shape for strict decoding: unknown
// keys are rejected, the same discipline as the config package (a
// typo'd field is a profile error, not a silent default).
type rawProfileSet struct {
	Kinds    map[string]rawProfile `yaml:"kinds"`
	Profiles map[string]rawProfile `yaml:"profiles"`
}

// rawProfile is both a kind-defaults entry and one component's
// profile; scalar fields are pointers so the merge can distinguish
// "profile overrides with zero value" from "absent, inherit the kind
// default" (AD-38 decision 3's kind-level-defaults + per-profile
// override semantics).
type rawProfile struct {
	Kind string  `yaml:"kind"`
	Hard rawHard `yaml:"hard"`
	Soft rawSoft `yaml:"soft"`
}

type rawHard struct {
	MinCPUCores *int     `yaml:"min_cpu_cores"`
	MinMemoryMB *int     `yaml:"min_memory_mb"`
	MinDiskGB   *int     `yaml:"min_disk_gb"`
	Arch        []string `yaml:"arch"`
	NeedsEgress *bool    `yaml:"needs_egress"`
	Ports       []string `yaml:"ports"`
}

type rawSoft struct {
	PreferredZone *string `yaml:"preferred_zone"`
	Spread        *string `yaml:"spread"`
}

// Parse decodes and validates one profile-set document (the shipped
// profiles/components.yaml shape: a top-level profiles: map plus the
// optional kinds: map of family defaults). It returns the merged
// profiles keyed by component name; consumption order is the caller's
// (Evaluate sorts). Validation is fail-closed: schema violations,
// profile names outside render.Allowlist(), and kind references with
// no matching kinds entry all error, each naming the offender.
func Parse(data []byte) (map[string]Profile, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("%w: empty document", ErrInvalidProfiles)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("%w: invalid yaml: %v", ErrInvalidProfiles, err)
	}
	var raw rawProfileSet
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProfiles, err)
	}
	return mergeProfiles(raw)
}

// mergeProfiles validates every declared value (including values a
// profile-level override would shadow — review-gated data stays clean
// of dead weight), checks names against the render allowlist, then
// folds kind defaults under profile overrides.
func mergeProfiles(raw rawProfileSet) (map[string]Profile, error) {
	for name, k := range raw.Kinds {
		if msg := rawChecks(k); msg != "" {
			return nil, fmt.Errorf("%w: kind %q: %s", ErrInvalidProfiles, name, msg)
		}
	}
	for name, p := range raw.Profiles {
		if msg := rawChecks(p); msg != "" {
			return nil, fmt.Errorf("%w: profile %q: %s", ErrInvalidProfiles, name, msg)
		}
		if p.Kind != "" {
			if _, ok := raw.Kinds[p.Kind]; !ok {
				return nil, fmt.Errorf("%w: profile %q references kind %q, which is not defined", ErrUnknownKind, name, p.Kind)
			}
		}
	}

	allowlist := render.Allowlist()
	var offenders []string
	for name := range raw.Profiles {
		if !slices.Contains(allowlist, name) {
			offenders = append(offenders, name)
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		return nil, fmt.Errorf("%w: %s (allowlist: %s)",
			ErrUnknownProfileName, strings.Join(offenders, ", "), strings.Join(allowlist, ", "))
	}

	out := make(map[string]Profile, len(raw.Profiles))
	for name, p := range raw.Profiles {
		merged := Profile{Name: name, Kind: p.Kind}
		if p.Kind != "" {
			merged = mergeKind(name, raw.Kinds[p.Kind], p)
		} else {
			merged.Hard = resolvedHard(p.Hard)
			merged.Soft = resolvedSoft(p.Soft)
		}
		out[name] = merged
	}
	return out, nil
}

// rawChecks validates one raw entry's declared values against the
// shared vocabularies. It returns "" when valid, else the message.
func rawChecks(p rawProfile) string {
	if msg := rawHardChecks(p.Hard); msg != "" {
		return msg
	}
	return rawSoftChecks(p.Soft)
}

// rawHardChecks validates the floors and the arch/ports vocabularies,
// including values a profile-level override would shadow — review-
// gated data stays clean of dead weight.
func rawHardChecks(h rawHard) string {
	for _, field := range []struct {
		name string
		val  *int
	}{
		{"min_cpu_cores", h.MinCPUCores},
		{"min_memory_mb", h.MinMemoryMB},
		{"min_disk_gb", h.MinDiskGB},
	} {
		if field.val != nil && *field.val < 0 {
			return fmt.Sprintf("hard.%s is %d, must be >= 0", field.name, *field.val)
		}
	}
	for _, arch := range h.Arch {
		if arch != config.ArchX8664 && arch != config.ArchARM64 {
			return fmt.Sprintf("hard.arch %q must be %q or %q", arch, config.ArchX8664, config.ArchARM64)
		}
	}
	for _, port := range h.Ports {
		if !portNamePattern.MatchString(port) {
			return fmt.Sprintf("hard.ports entry %q is not [a-z0-9_]", port)
		}
	}
	return ""
}

// rawSoftChecks validates the soft vocabulary: zone and spread.
func rawSoftChecks(s rawSoft) string {
	if zone := s.PreferredZone; zone != nil && *zone != "" &&
		*zone != config.ZoneCloud && *zone != config.ZoneLAN {
		return fmt.Sprintf("soft.preferred_zone %q must be %q or %q", *zone, config.ZoneCloud, config.ZoneLAN)
	}
	if spread := s.Spread; spread != nil && *spread != "" && *spread != SpreadComponent {
		return fmt.Sprintf("soft.spread %q must be %q", *spread, SpreadComponent)
	}
	return ""
}

// mergeKind folds one kind's defaults under a profile's overrides:
// profile scalars win when present, kind values fill the gaps; a
// profile's non-empty list replaces the kind's list, an empty one
// inherits (override semantics, not union — AD-38 decision 3). Slices
// are cloned so profiles never alias kind-owned arrays.
func mergeKind(name string, kind, p rawProfile) Profile {
	merged := Profile{Name: name, Kind: p.Kind}
	merged.Hard = resolvedHard(kind.Hard)
	merged.Soft = resolvedSoft(kind.Soft)
	if v := p.Hard.MinCPUCores; v != nil {
		merged.Hard.MinCPUCores = *v
	}
	if v := p.Hard.MinMemoryMB; v != nil {
		merged.Hard.MinMemoryMB = *v
	}
	if v := p.Hard.MinDiskGB; v != nil {
		merged.Hard.MinDiskGB = *v
	}
	if p.Hard.Arch != nil {
		merged.Hard.Arch = slices.Clone(p.Hard.Arch)
	}
	if v := p.Hard.NeedsEgress; v != nil {
		merged.Hard.NeedsEgress = *v
	}
	if p.Hard.Ports != nil {
		merged.Hard.Ports = slices.Clone(p.Hard.Ports)
	}
	if v := p.Soft.PreferredZone; v != nil {
		merged.Soft.PreferredZone = *v
	}
	if v := p.Soft.Spread; v != nil {
		merged.Soft.Spread = *v
	}
	return merged
}

// resolvedHard converts an override-free raw entry (a profile with no
// kind) into the resolved shape, cloning the slices the caller owns.
func resolvedHard(h rawHard) Hard {
	out := Hard{NeedsEgress: h.NeedsEgress != nil && *h.NeedsEgress}
	if v := h.MinCPUCores; v != nil {
		out.MinCPUCores = *v
	}
	if v := h.MinMemoryMB; v != nil {
		out.MinMemoryMB = *v
	}
	if v := h.MinDiskGB; v != nil {
		out.MinDiskGB = *v
	}
	out.Arch = slices.Clone(h.Arch)
	out.Ports = slices.Clone(h.Ports)
	return out
}

// resolvedSoft converts an override-free raw soft entry into the
// resolved shape.
func resolvedSoft(s rawSoft) Soft {
	out := Soft{}
	if v := s.PreferredZone; v != nil {
		out.PreferredZone = *v
	}
	if v := s.Spread; v != nil {
		out.Spread = *v
	}
	return out
}
