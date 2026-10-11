// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"fmt"
	"slices"
	"sort"

	"github.com/kikakkz/looming/platform/go/config"
	"github.com/kikakkz/looming/platform/go/render"
)

// Rule names one evaluator hard constraint (advisor-l1 §5, one rule
// per constraint, each independently unit-tested across the full
// violation matrix). The names are the operator-facing vocabulary the
// table mode and the session log quote.
type Rule string

const (
	RuleResourceFloor Rule = "resource-floor"
	RuleEgress        Rule = "egress"
	RuleArch          Rule = "arch"
	RulePort          Rule = "port"
)

// Missing-fact vocabulary (advisor-l1 §5 fact completeness): the names
// the evaluator quotes when a hard-rule input is absent from the host
// declaration. A zero-valued scalar fact reads as "not declared" —
// phase-1 hand-declared facts carry no other zero meaning.
const (
	FactCPUCores = "hardware.cpu_cores"
	FactMemoryMB = "hardware.memory_mb"
	FactDiskGB   = "hardware.disk_gb"
	FactArch     = "hardware.arch"
	FactEgress   = "network.egress"
)

// RuleViolation is one hard rule the (component, host) pair breaks,
// with the operator-facing detail. Violations and missing facts are
// disjoint: a violation means the fact was declared and fails the
// rule, a missing entry means the rule needed a fact the host does
// not declare.
type RuleViolation struct {
	Rule   Rule
	Detail string
}

// Headroom is the pair's remaining capacity after the profile's
// floors: the bin-packing figures the table mode ranks by. It is nil
// unless the host declared both facts — it cannot be computed from
// undeclared facts. It is computed for infeasible pairs too (negative
// when a floor fails): the table's host ordering ranks by headroom
// regardless of feasibility, while the display only quotes it on
// FEASIBLE rows (advisor-l1 §4).
type Headroom struct {
	MemoryMB int
	CPUCores int
}

// Verdict is the feasibility decision for one (component, host) pair:
// feasible exactly when no hard rule fires and no needed fact is
// missing. Ports is the profile's claimed port set — the evaluator
// reports it, never arbitrates conflicts: the Declare surface keeps
// that authority (advisor-l1 §5).
type Verdict struct {
	Component  string
	Host       string
	Feasible   bool
	Violations []RuleViolation
	Missing    []string
	Ports      []string
	Headroom   *Headroom
}

// Evaluate computes the full feasibility matrix: one verdict per
// (profile, host) pair — components in sorted-name order, hosts in
// declaration order, so the output is deterministic for a given input.
// It is a pure function: no I/O, no model access, no global state.
func Evaluate(hosts []config.Host, profiles map[string]Profile) []Verdict {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	verdicts := make([]Verdict, 0, len(names)*len(hosts))
	for _, name := range names {
		for _, h := range hosts {
			verdicts = append(verdicts, evaluatePair(profiles[name], h))
		}
	}
	return verdicts
}

// evaluatePair applies every hard rule to one pair. Each rule lives
// in its own function — one rule per hard constraint, the shape
// advisor-l1 §5 asks for — and follows the same fail-closed contract:
// a non-zero profile constraint makes the corresponding fact
// mandatory; absent means missing, present-and-failing means
// violation.
func evaluatePair(p Profile, h config.Host) Verdict {
	v := Verdict{Component: p.Name, Host: h.ID}
	v.Ports = sortedStrings(p.Hard.Ports)

	var violations []RuleViolation
	var missing []string

	violations = append(violations, checkPorts(p)...)
	violations, missing = checkResourceFloor(p, h, violations, missing)
	violations, missing = checkEgress(p, h, violations, missing)
	violations, missing = checkArch(p, h, violations, missing)

	v.Feasible = len(violations) == 0 && len(missing) == 0
	v.Violations = violations
	v.Missing = sortedStrings(missing)
	v.Headroom = headroom(p, h)
	return v
}

// checkPorts is the ports rule: claimed port names must exist in the
// component's render contract. The evaluator only reports the claimed
// set — conflict arbitration stays with the Declare surface
// (advisor-l1 §5).
func checkPorts(p Profile) []RuleViolation {
	contract, _ := render.Lookup(p.Name)
	var violations []RuleViolation
	for _, port := range p.Hard.Ports {
		if contract.ListenPort == "" || port != contract.ListenPort {
			violations = append(violations, RuleViolation{
				Rule:   RulePort,
				Detail: fmt.Sprintf("port %q is not in %s's render contract", port, p.Name),
			})
		}
	}
	return violations
}

// checkResourceFloor is the resource-floors rule: cpu, memory, disk —
// each an independent rule instance so the violation matrix stays
// per-fact.
func checkResourceFloor(p Profile, h config.Host, violations []RuleViolation, missing []string) ([]RuleViolation, []string) {
	if p.Hard.MinCPUCores > 0 {
		switch hw := h.Capabilities; {
		case hw == nil || hw.Hardware.CPUCores == 0:
			missing = append(missing, FactCPUCores)
		case hw.Hardware.CPUCores < p.Hard.MinCPUCores:
			violations = append(violations, RuleViolation{
				Rule:   RuleResourceFloor,
				Detail: fmt.Sprintf("host declares %d cpu cores, profile requires at least %d", hw.Hardware.CPUCores, p.Hard.MinCPUCores),
			})
		}
	}
	if p.Hard.MinMemoryMB > 0 {
		switch hw := h.Capabilities; {
		case hw == nil || hw.Hardware.MemoryMB == 0:
			missing = append(missing, FactMemoryMB)
		case hw.Hardware.MemoryMB < p.Hard.MinMemoryMB:
			violations = append(violations, RuleViolation{
				Rule:   RuleResourceFloor,
				Detail: fmt.Sprintf("host declares %d MB memory, profile requires at least %d", hw.Hardware.MemoryMB, p.Hard.MinMemoryMB),
			})
		}
	}
	if p.Hard.MinDiskGB > 0 {
		switch hw := h.Capabilities; {
		case hw == nil || hw.Hardware.DiskGB == 0:
			missing = append(missing, FactDiskGB)
		case hw.Hardware.DiskGB < p.Hard.MinDiskGB:
			violations = append(violations, RuleViolation{
				Rule:   RuleResourceFloor,
				Detail: fmt.Sprintf("host declares %d GB disk, profile requires at least %d", hw.Hardware.DiskGB, p.Hard.MinDiskGB),
			})
		}
	}
	return violations, missing
}

// checkEgress is the egress rule: a declared false fails the rule; an
// undeclared fact is the named gap (the pointer exists precisely to
// keep the two apart — config.Network.Egress's three-state contract).
func checkEgress(p Profile, h config.Host, violations []RuleViolation, missing []string) ([]RuleViolation, []string) {
	if !p.Hard.NeedsEgress {
		return violations, missing
	}
	switch nw := h.Capabilities; {
	case nw == nil || nw.Network.Egress == nil:
		missing = append(missing, FactEgress)
	case !*nw.Network.Egress:
		violations = append(violations, RuleViolation{
			Rule:   RuleEgress,
			Detail: fmt.Sprintf("profile requires egress, host %q declares none", h.ID),
		})
	}
	return violations, missing
}

// checkArch is the architecture rule: the host's declared arch must
// be one the profile permits. An empty profile list declares no
// constraint.
func checkArch(p Profile, h config.Host, violations []RuleViolation, missing []string) ([]RuleViolation, []string) {
	if len(p.Hard.Arch) == 0 {
		return violations, missing
	}
	switch hw := h.Capabilities; {
	case hw == nil || hw.Hardware.Arch == "":
		missing = append(missing, FactArch)
	case !slices.Contains(p.Hard.Arch, hw.Hardware.Arch):
		violations = append(violations, RuleViolation{
			Rule:   RuleArch,
			Detail: fmt.Sprintf("host arch %q is not in the profile's %v", hw.Hardware.Arch, p.Hard.Arch),
		})
	}
	return violations, missing
}

// headroom computes the pair's remaining capacity after the profile's
// floors, feasible or not — infeasible pairs can read negative (a
// failed floor), which is exactly the ranking signal the table's host
// ordering consumes. Both facts must be declared; anything else
// leaves it nil (the table renders "n/a" on feasible rows, and ranks
// fact-less pairs last either way).
func headroom(p Profile, h config.Host) *Headroom {
	hw := h.Capabilities
	if hw == nil || hw.Hardware.MemoryMB == 0 || hw.Hardware.CPUCores == 0 {
		return nil
	}
	return &Headroom{
		MemoryMB: hw.Hardware.MemoryMB - p.Hard.MinMemoryMB,
		CPUCores: hw.Hardware.CPUCores - p.Hard.MinCPUCores,
	}
}

// sortedStrings returns a sorted copy of in (nil stays nil) — the
// deterministic output the table and the session log quote.
func sortedStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := slices.Clone(in)
	sort.Strings(out)
	return out
}
