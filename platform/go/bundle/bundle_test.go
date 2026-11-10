// SPDX-License-Identifier: Apache-2.0

package bundle_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/bundle"
	"github.com/kikakkz/looming/platform/go/render"
)

const validManifest = `
components:
  gateway: v0.1.0
  identity: v0.2.3
  topology: v0.1.0
`

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadValidManifest(t *testing.T) {
	m, err := bundle.Load(writeManifest(t, validManifest))
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, map[string]string{
		"gateway":  "v0.1.0",
		"identity": "v0.2.3",
		"topology": "v0.1.0",
	}, m.Components)
}

func TestPinsResolveFullImageRefs(t *testing.T) {
	m, err := bundle.Load(writeManifest(t, validManifest))
	require.NoError(t, err)
	pins := m.Pins()
	assert.Equal(t, "ghcr.io/kikakkz/looming/gateway:v0.1.0", pins["gateway"])
	assert.Equal(t, "ghcr.io/kikakkz/looming/identity:v0.2.3", pins["identity"])
	assert.Equal(t, "ghcr.io/kikakkz/looming/topology:v0.1.0", pins["topology"])
	assert.Len(t, pins, 3)
}

func TestLoadRejectsUnknownTopLevelKey(t *testing.T) {
	_, err := bundle.Load(writeManifest(t, "components: {gateway: v0.1.0}\ndigest: sha256:abc\n"))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrInvalid)
}

func TestLoadRejectsMissingComponents(t *testing.T) {
	_, err := bundle.Load(writeManifest(t, "# nothing here\n"))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrInvalid)
}

func TestLoadRejectsEmptyComponents(t *testing.T) {
	_, err := bundle.Load(writeManifest(t, "components: {}\n"))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrInvalid)
}

func TestLoadRejectsUnknownComponent(t *testing.T) {
	_, err := bundle.Load(writeManifest(t, "components: {vault: v0.1.0}\n"))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrUnknownComponent)
	assert.Contains(t, err.Error(), "vault")
}

func TestLoadRejectsBadVersions(t *testing.T) {
	cases := []struct {
		name    string
		version string
	}{
		{"missing v prefix", "0.1.0"},
		{"two segments", "v0.1"},
		{"four segments", "v0.1.0.1"},
		{"empty", ""},
		{"non numeric", "v0.1.x"},
		{"leading v only", "v"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := "components: {gateway: " + tc.version + "}\n"
			_, err := bundle.Load(writeManifest(t, doc))
			require.Error(t, err)
			assert.ErrorIs(t, err, bundle.ErrInvalidVersion)
			assert.Contains(t, err.Error(), "gateway")
		})
	}
}

func TestLoadMissingFileFailsClosed(t *testing.T) {
	_, err := bundle.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrUnreadable)
}

func TestLoadRejectsInvalidYAML(t *testing.T) {
	_, err := bundle.Load(writeManifest(t, "components: [\n"))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrInvalid)
}

// TestValidVersion pins the vX.Y.Z shape both the manifest loader and
// config's bundle-section validation share (the release workflow
// accepts the same shape, .github/workflows/release.yml).
func TestValidVersion(t *testing.T) {
	assert.True(t, bundle.ValidVersion("v0.1.0"))
	assert.True(t, bundle.ValidVersion("v10.20.30"))
	for _, bad := range []string{"", "0.1.0", "v0.1", "v0.1.0.1", "vx.y.z", "v0.1.x"} {
		assert.False(t, bundle.ValidVersion(bad), bad)
	}
}

// TestRepoManifestLoads guards the manifest shipped at the bundle
// root: it must parse under the same loader apply uses, pin every
// release component (and only those), and resolve to the release
// registry refs.
func TestRepoManifestLoads(t *testing.T) {
	m, err := bundle.Load(filepath.Join("..", "..", "..", "bundle", "manifest.yaml"))
	require.NoError(t, err)

	want := render.ReleaseNames()
	keys := make([]string, 0, len(m.Components))
	for release, ref := range m.Pins() {
		keys = append(keys, release)
		assert.Equal(t, render.ImageRef(release, m.Components[release]), ref)
	}
	sort.Strings(keys)
	assert.Equal(t, want, keys,
		"the shipped manifest pins every release component, and only those")
}

const validManifestWithUpstream = `
components:
  gateway: v0.1.0
  identity: v0.2.3
  topology: v0.1.0
upstream:
  headscale: headscale/headscale:0.29.6
`

func TestLoadUpstreamPins(t *testing.T) {
	m, err := bundle.Load(writeManifest(t, validManifestWithUpstream))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"headscale": "headscale/headscale:0.29.6",
	}, m.Upstream)
	assert.Equal(t, m.Upstream, m.UpstreamPins())
}

func TestLoadUpstreamOptional(t *testing.T) {
	m, err := bundle.Load(writeManifest(t, validManifest))
	require.NoError(t, err)
	assert.Empty(t, m.Upstream)
	assert.Empty(t, m.UpstreamPins())
}

func TestLoadRejectsLatestUpstreamTag(t *testing.T) {
	doc := "components: {gateway: v0.1.0}\nupstream: {headscale: headscale/headscale:latest}\n"
	_, err := bundle.Load(writeManifest(t, doc))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrInvalidUpstream)
}

func TestLoadRejectsDuplicateUpstreamKey(t *testing.T) {
	// yaml.v3 refuses duplicate mapping keys; the release scan holds
	// the same line, so a duplicated pin must never ship. The loader's
	// rejection rides the generic decode error (ErrInvalid).
	doc := `
components:
  gateway: v0.1.0
upstream:
  headscale: headscale/headscale:0.29.6
  headscale: headscale/headscale:0.29.7
`
	_, err := bundle.Load(writeManifest(t, doc))
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrInvalid)
}

func TestLoadRejectsMalformedUpstreamRef(t *testing.T) {
	cases := []struct {
		name string
		ref  string
	}{
		{"no tag", "headscale/headscale"},
		{"empty tag", "headscale/headscale:"},
		{"uppercase repo", "Headscale/Headscale:0.29.6"},
		{"whitespace", "head scale/headscale:0.29.6"},
		{"double colon", "headscale/headscale:0.29.6:extra"},
		{"digest instead of tag", "headscale/headscale@sha256:abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := "components: {gateway: v0.1.0}\nupstream: {svc: " + tc.ref + "}\n"
			_, err := bundle.Load(writeManifest(t, doc))
			require.Error(t, err)
			assert.ErrorIs(t, err, bundle.ErrInvalidUpstream)
		})
	}
}

func TestLoadAcceptsDockerReferenceSeparators(t *testing.T) {
	// The loader's ref rule is intentionally identical to the release
	// scan's (review on #210): repeated reference separators that
	// docker accepts must pass both gates, or a manifest the release
	// publishes fails to load at apply time.
	doc := `
components:
  gateway: v0.1.0
upstream:
  headscale: registry.example.com/head--scale:0.29.6
  derper: ghcr.io/a__b/derper:1.0
`
	m, err := bundle.Load(writeManifest(t, doc))
	require.NoError(t, err)
	assert.Len(t, m.Upstream, 2)
}

func TestLoadRejectsBadUpstreamName(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"uppercase", "Headscale"},
		{"underscore", "head_scale"},
		{"leading digit", "0headscale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := "components: {gateway: v0.1.0}\nupstream: {" + tc.key + ": headscale/headscale:0.29.6}\n"
			_, err := bundle.Load(writeManifest(t, doc))
			require.Error(t, err)
			assert.ErrorIs(t, err, bundle.ErrInvalidUpstream)
		})
	}
}

// TestRepoManifestUpstream guards the shipped upstream section (AD-41):
// present, explicit-version, and shared by the release workflow's own
// scan (.github/workflows/release.yml).
func TestRepoManifestUpstream(t *testing.T) {
	m, err := bundle.Load(filepath.Join("..", "..", "..", "bundle", "manifest.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, m.Upstream, "the shipped manifest pins the upstream control plane (AD-41)")
	for name, ref := range m.Upstream {
		assert.NotContains(t, ref, ":latest", "upstream pin %s must not use the mutable latest tag", name)
	}
}
