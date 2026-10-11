// SPDX-License-Identifier: Apache-2.0

// Package bundle loads the release bundle manifest (release-l1 §3.4):
// bundle/manifest.yaml pins every phase-1 component's exact image
// version, and apply resolves those pins at render time so a released
// combination converges published images only — never a source build
// (#160, slice R2). Decoding is strict: an unknown key, an unknown
// component, or a malformed version is a manifest error, not a silent
// default — the operator's install fails closed at apply time.
package bundle

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kikakkz/looming/platform/go/render"
)

// Manifest-level sentinels, errors.Is-able by apply's reporting.
var (
	ErrUnreadable       = errors.New("bundle: cannot read manifest")
	ErrInvalid          = errors.New("bundle: invalid manifest")
	ErrUnknownComponent = errors.New("bundle: unknown component (phase-1 allowlist)")
	ErrInvalidVersion   = errors.New("bundle: version must be a v-prefixed semver tag (vX.Y.Z)")
)

// versionPattern is the vX.Y.Z shape the release workflow accepts for
// component tags (.github/workflows/release.yml) — the manifest pins
// the same tags CI publishes, and nothing else.
var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// ValidVersion reports whether v carries the manifest's vX.Y.Z tag
// shape. Config's optional topology bundle: section shares it, so a
// typo'd bundle version fails at config validation, not in a report.
func ValidVersion(v string) bool { return versionPattern.MatchString(v) }

// Manifest is the decoded bundle manifest: the released component
// pins, keyed by release name (the render contracts' ReleaseName
// values — gateway, identity, topology; release-l1 §3.4).
type Manifest struct {
	Components map[string]string
}

// raw mirrors the YAML shape for strict decoding: exactly one
// components key, nothing else.
type rawManifest struct {
	Components map[string]string `yaml:"components"`
}

// Load reads, strictly decodes, and validates the manifest at path.
// Every failure — unreadable file, YAML syntax, unknown key, unknown
// component, malformed version, or an empty pin set — is one manifest
// error naming the file (and the offending component where known).
func Load(path string) (*Manifest, error) {
	//nolint:gosec // the path comes from the operator's topology.yaml; reading exactly what was named is the command's job.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w %q: %v", ErrUnreadable, path, err)
	}

	var raw rawManifest
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w %q: %v", ErrInvalid, path, err)
	}
	if len(raw.Components) == 0 {
		return nil, fmt.Errorf("%w %q: components is required and must pin at least one component", ErrInvalid, path)
	}

	allowed := map[string]bool{}
	for _, name := range render.ReleaseNames() {
		allowed[name] = true
	}
	components := make(map[string]string, len(raw.Components))
	for name, version := range raw.Components {
		if !allowed[name] {
			return nil, fmt.Errorf("%w %q: %q (release components: %s)", ErrUnknownComponent, path, name, strings.Join(render.ReleaseNames(), ", "))
		}
		if !ValidVersion(version) {
			return nil, fmt.Errorf("%w %q: %s = %q", ErrInvalidVersion, path, name, version)
		}
		components[name] = version
	}
	return &Manifest{Components: components}, nil
}

// Pins resolves every pin into the full image ref apply hands the
// renderer (release name → ghcr ref, via render.ImageRef), keyed by
// the manifest's own release-name vocabulary. Apply maps placements
// onto these names through the render contracts' ReleaseName.
func (m *Manifest) Pins() map[string]string {
	pins := make(map[string]string, len(m.Components))
	for release, version := range m.Components {
		pins[release] = render.ImageRef(release, version)
	}
	return pins
}
