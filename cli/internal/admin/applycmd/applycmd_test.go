// SPDX-License-Identifier: Apache-2.0

package applycmd

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/apply"
	"github.com/kikakkz/looming/platform/go/render"
)

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func runWith(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := New(&out, testLog())
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

const testConfig = `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.11}]
placements:
  - {component: gateway-front, host: only, ports: {http: 8080}, config: {upstream: "http://10.0.0.13:4000"}}
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestApplyRejectsEmptyConfigFlag(t *testing.T) {
	_, err := runWith(t, "--config", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--config must not be empty")
}

func TestApplyMissingConfigSurfacesConfigError(t *testing.T) {
	_, err := runWith(t, "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config:")
	assert.Contains(t, err.Error(), "missing.yaml")
}

func TestApplyRejectsBadBundleRoot(t *testing.T) {
	path := writeConfig(t, testConfig)
	_, err := runWith(t, "--config", path, "--bundle-root", filepath.Join(t.TempDir(), "nope"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--bundle-root")
}

func TestApplyDryRunPrintsComposeWithoutSideEffects(t *testing.T) {
	path := writeConfig(t, testConfig)
	out, err := runWith(t, "--config", path, "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "dry-run: rendered compose files")
	assert.Contains(t, out, "--- only ")
	assert.Contains(t, out, "gateway-front:")
	assert.Contains(t, out, "GATEWAY_UPSTREAM: http://10.0.0.13:4000")
	assert.Contains(t, out, "re-run without --dry-run")
	assert.NotContains(t, out, "topology revision")
}

func TestApplyRelativeConfigSurvivesBundleRootChdir(t *testing.T) {
	// The config path resolves against the operator's cwd, not the
	// bundle root the process chdirs into.
	dir := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	require.NoError(t, os.WriteFile(filepath.Join(dir, "topology.yaml"), []byte(testConfig), 0o600))
	bundleRoot := t.TempDir()

	out, err := runWith(t, "--config", "./topology.yaml", "--bundle-root", bundleRoot, "--dry-run")
	require.NoError(t, err, "a relative --config must resolve before the chdir")
	assert.Contains(t, out, "gateway-front:")
}

func TestApplyWithoutStatePlaneDemandsDatabaseURL(t *testing.T) {
	path := writeConfig(t, testConfig)
	out, err := runWith(t, "--config", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TOPOLOGY_DATABASE_URL")
	assert.Empty(t, out)
}

func TestApplyHelpListsFlags(t *testing.T) {
	cmd := New(io.Discard, testLog())
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	usage := cmd.Flags().FlagUsages()
	for _, want := range []string{"--config", "--bundle-root", "--dry-run", "--print-invite"} {
		assert.Contains(t, usage, want)
	}
}

// TestPrintSummaryCoversEveryOutcomeBranch pins the operator-facing
// summary rendering: per-host outcomes, the invite trio, and the guide
// trio. Pure functions of the result struct — the converge itself is
// the pipeline's and the e2e suite's job.
func TestPrintSummaryCoversEveryOutcomeBranch(t *testing.T) {
	t.Run("dry-run prints artifacts", func(t *testing.T) {
		var out bytes.Buffer
		printSummary(&out, &apply.Result{
			DryRun: true,
			Artifacts: []render.Artifact{
				{HostID: "only", Compose: "services: {}\n", Hash: "sha256:x"},
			},
		}, true)
		s := out.String()
		assert.Contains(t, s, "dry-run: rendered compose files")
		assert.Contains(t, s, "--- only (sha256:x)")
		assert.Contains(t, s, "re-run without --dry-run")
	})

	t.Run("host outcomes and nil invite/guide", func(t *testing.T) {
		var out bytes.Buffer
		printSummary(&out, &apply.Result{
			Revision: 7,
			Hosts: []apply.HostResult{
				{HostID: "h1", Changed: true},
				{HostID: "h2", Skipped: true},
				{HostID: "h3", Err: assert.AnError},
			},
		}, false)
		s := out.String()
		assert.Contains(t, s, "topology revision 7")
		assert.Contains(t, s, "host h1: changed (compose converge ran)")
		assert.Contains(t, s, "host h2: skipped (unchanged)")
		assert.Contains(t, s, "host h3: FAILED")
		assert.Contains(t, s, "looming token create --role engine")
	})

	t.Run("invite outcomes", func(t *testing.T) {
		cases := map[string]struct {
			inv   *apply.InviteOutcome
			wants []string
		}{
			"printed": {
				inv:   &apply.InviteOutcome{Printed: true, AdminEmail: "a@b.c", Token: "tok", ExpiresAt: "2027-01-01T00:00:00Z", Endpoint: "http://10.0.0.1", RegisterPath: "/v1/self/register"},
				wants: []string{"bootstrap invite for a@b.c", "token: tok", "expires_at:", "invite_token"},
			},
			"skipped": {
				inv:   &apply.InviteOutcome{Skipped: true, Reason: "nothing changed"},
				wants: []string{"bootstrap invite: skipped (nothing changed)"},
			},
			"warning": {
				inv:   &apply.InviteOutcome{Warning: true, Reason: "identityd said 500"},
				wants: []string{"bootstrap invite: WARNING: identityd said 500"},
			},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				var out bytes.Buffer
				printInviteOutcome(&out, tc.inv)
				for _, want := range tc.wants {
					assert.Contains(t, out.String(), want)
				}
			})
		}
		var out bytes.Buffer
		printInviteOutcome(&out, nil)
		assert.Empty(t, out.String())
	})

	t.Run("guide outcomes", func(t *testing.T) {
		var out bytes.Buffer
		printGuideOutcome(&out, &apply.GuideOutcome{Rendered: true})
		assert.Contains(t, out.String(), "guide: rendered")
		out.Reset()
		printGuideOutcome(&out, &apply.GuideOutcome{Err: assert.AnError})
		assert.Contains(t, out.String(), "guide: WARNING:")
		out.Reset()
		printGuideOutcome(&out, &apply.GuideOutcome{})
		assert.Contains(t, out.String(), "guide: unchanged")
		out.Reset()
		printGuideOutcome(&out, nil)
		assert.Empty(t, out.String())
	})
}
