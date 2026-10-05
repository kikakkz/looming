// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func runWith(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(context.Background(), args, &out, &errOut, testLog())
	return out.String() + errOut.String(), err
}

const ctlTestConfig = `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.11}]
placements:
  - {component: gateway-front, host: only, ports: {http: 8080}, config: {upstream: "http://10.0.0.13:4000"}}
`

func writeCtlConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestApplyRejectsEmptyConfigFlag(t *testing.T) {
	_, err := runWith(t, "apply", "--config", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--config must not be empty")
}

func TestApplyMissingConfigSurfacesConfigError(t *testing.T) {
	_, err := runWith(t, "apply", "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config:")
	assert.Contains(t, err.Error(), "missing.yaml")
}

func TestApplyRejectsBadBundleRoot(t *testing.T) {
	path := writeCtlConfig(t, ctlTestConfig)
	_, err := runWith(t, "apply", "--config", path, "--bundle-root", filepath.Join(t.TempDir(), "nope"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--bundle-root")
}

func TestApplyDryRunPrintsComposeWithoutSideEffects(t *testing.T) {
	path := writeCtlConfig(t, ctlTestConfig)
	out, err := runWith(t, "apply", "--config", path, "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "dry-run: rendered compose files")
	assert.Contains(t, out, "--- only ")
	assert.Contains(t, out, "gateway-front:")
	assert.Contains(t, out, "GATEWAY_UPSTREAM: http://10.0.0.13:4000")
	assert.Contains(t, out, "re-run without --dry-run")
	assert.NotContains(t, out, "revision")
}

func TestApplyRelativeConfigSurvivesBundleRootChdir(t *testing.T) {
	// The config path resolves against the operator's cwd, not the
	// bundle root the process chdirs into.
	dir := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	require.NoError(t, os.WriteFile(filepath.Join(dir, "topology.yaml"), []byte(ctlTestConfig), 0o600))
	bundleRoot := t.TempDir()

	out, err := runWith(t, "apply", "--config", "./topology.yaml", "--bundle-root", bundleRoot, "--dry-run")
	require.NoError(t, err, "a relative --config must resolve before the chdir")
	assert.Contains(t, out, "gateway-front:")
}

func TestApplyWithoutStatePlaneDemandsDatabaseURL(t *testing.T) {
	path := writeCtlConfig(t, ctlTestConfig)
	out, err := runWith(t, "apply", "--config", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TOPOLOGY_DATABASE_URL")
	assert.Empty(t, out)
}

func TestRootHelpListsApply(t *testing.T) {
	out, err := runWith(t, "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "apply")

	out, err = runWith(t, "apply", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "--config")
	assert.Contains(t, out, "--bundle-root")
	assert.Contains(t, out, "--dry-run")
}

func TestUnknownCommandFails(t *testing.T) {
	_, err := runWith(t, "frobnicate")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "unknown command") || strings.Contains(err.Error(), "frobnicate"))
}
