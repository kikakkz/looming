// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBundleManifestRender is the e2e-layer proof of release-l1 §3.5's
// render/apply contract (slice R2, #160): the REAL looming binary,
// given a topology carrying the installer-owned bundle section,
// resolves the manifest's pins and renders `image:` refs with NO build
// block — the artifact-only install surface. The control topology
// (no bundle section) renders the source-build path unchanged, which
// is the byte-identity evidence for the PR/e2e surface. Both halves
// drive only `apply --dry-run`: no docker, no registry, no pulls.
func TestBundleManifestRender(t *testing.T) {
	repoRoot := repoRoot(t)
	binDir := t.TempDir()
	cli := filepath.Join(binDir, "looming")
	buildCLI(t, repoRoot, cli)

	workDir := t.TempDir()
	manifest := filepath.Join(workDir, "bundle-manifest.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte(`
components:
  gateway: v0.1.0
`), 0o600))

	// The pinned topology: identical to the control except the bundle
	// section — the render difference must be exactly the pin.
	pinned := `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.11}]
placements:
  - {component: gateway-front, host: only, ports: {http: 18080}, config: {upstream: "http://10.0.0.13:4000"}}
bundle: {version: v0.1.0, manifest: ` + manifest + `}
`
	control := `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.11}]
placements:
  - {component: gateway-front, host: only, ports: {http: 18080}, config: {upstream: "http://10.0.0.13:4000"}}
`
	run := func(config string) string {
		t.Helper()
		path := filepath.Join(workDir, "topology.yaml")
		require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
		f := &fixture{repoRoot: repoRoot, bundleRoot: workDir, cli: cli, dockerEnv: os.Environ()}
		stdout, err := f.runCLI(t, 2*time.Minute,
			"apply", "--dry-run", "--config", path, "--bundle-root", repoRoot)
		require.NoError(t, err, "apply --dry-run failed:\n%s", stdout)
		return stdout
	}

	pinnedOut := run(pinned)
	assert.Contains(t, pinnedOut, "bundle: v0.1.0 (1 image pins from "+manifest+")",
		"the bundle selection passes through to the operator's report")
	assert.Contains(t, pinnedOut, "image: ghcr.io/kikakkz/looming/gateway:v0.1.0",
		"the manifest's pin renders as the service's image ref")
	assert.NotContains(t, pinnedOut, "build:",
		"a pinned render never builds (release-l1 §5 R2)")

	controlOut := run(control)
	assert.Contains(t, controlOut, "build:",
		"no bundle section → the source-build render is unchanged")
	assert.NotContains(t, controlOut, "image: ghcr.io")
	assert.NotContains(t, controlOut, "bundle:",
		"no bundle section → no bundle report line")
}
