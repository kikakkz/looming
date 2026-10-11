// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	padvisor "github.com/kikakkz/looming/platform/go/advisor"
	"github.com/kikakkz/looming/platform/go/config"
)

// updateGolden regenerates the render golden file: go test ./internal/advisor -update.
var updateGolden = flag.Bool("update", false, "regenerate golden files")

// fixtureTopology loads the testdata topology and evaluates the
// shipped profiles against it — the shared input of the golden render
// test and the command test.
func fixtureVerdicts(t *testing.T) []padvisor.Verdict {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", "topology.yaml"))
	require.NoError(t, err)
	profiles, err := padvisor.Parse(padvisor.DefaultProfilesYAML())
	require.NoError(t, err)
	return padvisor.Evaluate(cfg.Hosts, profiles)
}

func TestRenderTableGolden(t *testing.T) {
	got := renderTable(fixtureVerdicts(t))
	golden := filepath.Join("testdata", "advise_table.golden")
	if *updateGolden {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
	}
	want, err := os.ReadFile(golden) // #nosec G304 -- package-relative testdata golden, regenerated only via -update.
	require.NoError(t, err, "golden missing — regenerate with: go test ./internal/advisor -update")
	assert.Equal(t, string(want), got)
}

// splitLines drops the trailing empty element so line indices address
// real rows.
func splitLines(s string) []string {
	trimmed := strings.TrimSuffix(s, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// indexOf returns the first line carrying cell as a whitespace-delimited field.
func indexOf(lines []string, cell string) int {
	for i, line := range lines {
		if containsCell(line, cell) {
			return i
		}
	}
	return -1
}

// requireOrdered asserts both cells render on their own lines and that
// earlier's line precedes later's: indexOf's -1 miss must fail the
// test, not silently satisfy the ordering comparison.
func requireOrdered(t *testing.T, lines []string, earlier, later, msg string) {
	t.Helper()
	ei, li := indexOf(lines, earlier), indexOf(lines, later)
	require.NotEqual(t, -1, ei, "%s: %q missing from the table", msg, earlier)
	require.NotEqual(t, -1, li, "%s: %q missing from the table", msg, later)
	assert.Less(t, ei, li, msg)
}

func containsCell(line, cell string) bool {
	for _, field := range strings.Fields(line) {
		if field == cell {
			return true
		}
	}
	return false
}

// TestRenderTableOrdering pins the two ordering rules: components in
// allowlist order, hosts by memory headroom descending with CPU as
// tiebreak and unknown headroom last.
func TestRenderTableOrdering(t *testing.T) {
	verdicts := fixtureVerdicts(t)
	out := renderTable(verdicts)
	lines := splitLines(out)

	// Components appear in allowlist order: gateway-front before
	// identityd before topologyd.
	gwIdx, idIdx, tpIdx := indexOf(lines, "gateway-front"), indexOf(lines, "identityd"), indexOf(lines, "topologyd")
	require.NotEqual(t, -1, gwIdx)
	assert.Less(t, gwIdx, idIdx)
	assert.Less(t, idIdx, tpIdx)

	// Within gateway-front: gw-1 (feasible, headroom) precedes the two
	// infeasible pairs, which order by host id.
	gw1 := indexOf(lines, "gw-1")
	appUnderGw := -1
	for i := gwIdx; i < idIdx; i++ {
		if containsCell(lines[i], "app-1") {
			appUnderGw = i
		}
	}
	require.NotEqual(t, -1, appUnderGw)
	assert.Less(t, gw1, appUnderGw)

	// Memory-headroom ordering with a CPU tiebreak: two feasible hosts
	// with equal memory order by CPU desc.
	pairs := []padvisor.Verdict{
		{Component: "identityd", Host: "low", Feasible: true, Headroom: &padvisor.Headroom{MemoryMB: 2048, CPUCores: 1}},
		{Component: "identityd", Host: "high", Feasible: true, Headroom: &padvisor.Headroom{MemoryMB: 4096, CPUCores: 1}},
		{Component: "identityd", Host: "cpu", Feasible: true, Headroom: &padvisor.Headroom{MemoryMB: 4096, CPUCores: 3}},
		{Component: "identityd", Host: "none", Feasible: true},
	}
	lines = splitLines(renderTable(pairs))
	requireOrdered(t, lines, "cpu", "high", "memory tie breaks on cpu desc")
	requireOrdered(t, lines, "high", "low", "memory desc")
	requireOrdered(t, lines, "low", "none", "unknown headroom last")

	// Infeasible pairs rank by the same headroom convention (advisor-l1
	// §4's ordering is stated for hosts, not only feasible rows): two
	// egress-false hosts order by declared memory desc, not host id.
	infeasible := []padvisor.Verdict{
		{Component: "identityd", Host: "z-small", Feasible: false, Headroom: &padvisor.Headroom{MemoryMB: 512, CPUCores: -1}},
		{Component: "identityd", Host: "a-big", Feasible: false, Headroom: &padvisor.Headroom{MemoryMB: 4096, CPUCores: 2}},
	}
	lines = splitLines(renderTable(infeasible))
	requireOrdered(t, lines, "a-big", "z-small", "infeasible pairs rank by memory headroom")
}

// TestBuildSessionRecord pins the record's shape: the file path, the
// profile hash, the facts snapshot, and the matrix summary.
func TestBuildSessionRecord(t *testing.T) {
	cfg, err := config.Load(filepath.Join("testdata", "topology.yaml"))
	require.NoError(t, err)
	verdicts := fixtureVerdicts(t)

	rec := buildSessionRecord("/etc/looming/topology.yaml", cfg.Hosts, verdicts,
		profileSHA256(padvisor.DefaultProfilesYAML()), time.Date(2026, 10, 9, 12, 34, 56, 0, time.UTC))

	assert.Equal(t, "2026-10-09T12:34:56Z", rec.Timestamp)
	assert.Equal(t, "/etc/looming/topology.yaml", rec.File)
	assert.Equal(t, profileSHA256(padvisor.DefaultProfilesYAML()), rec.ProfileSHA256)
	assert.Len(t, rec.FactsSnapshot, 3)
	assert.Nil(t, rec.FactsSnapshot[2].Capabilities)
	assert.Equal(t, matrixSummary{Pairs: 9, Feasible: 4, Infeasible: 5}, rec.Matrix)
	assert.Equal(t, decisionTableViewed, rec.Decision)
}
