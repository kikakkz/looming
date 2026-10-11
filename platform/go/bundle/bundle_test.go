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
