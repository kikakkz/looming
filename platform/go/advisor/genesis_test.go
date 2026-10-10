// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	t0 = time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t1.Add(time.Hour)
)

// TestGenesisStateTransitions: each transition stamps its evidence and
// the switch flips the stage — the pure lifecycle #143 describes.
func TestGenesisStateTransitions(t *testing.T) {
	state := GenesisState{Stage: StageDirect}
	assert.Equal(t, StageDirect, state.Stage)

	state = state.RecordConvergence(t0)
	assert.Equal(t, t0.Format(time.RFC3339), state.ConvergedAt)

	state = state.MarkEnvWritten(t1)
	assert.Equal(t, t1.Format(time.RFC3339), state.EnvWrittenAt)

	state = state.MarkSwitched(t2)
	assert.Equal(t, StageGateway, state.Stage)
	assert.Equal(t, t2.Format(time.RFC3339), state.SwitchedAt)

	state = state.MarkErased(t2)
	assert.Equal(t, t2.Format(time.RFC3339), state.ErasedAt)
}

// TestGenesisStateCarriesNoSecrets: the persisted lifecycle state must
// be quotable anywhere — the api key (and any credential) never enters
// it. This is #143's minimization made structural.
func TestGenesisStateCarriesNoSecrets(t *testing.T) {
	state := GenesisState{Stage: StageDirect}.
		RecordConvergence(t0).
		MarkEnvWritten(t1).
		MarkSwitched(t2).
		MarkErased(t2)
	data, err := json.Marshal(state)
	require.NoError(t, err)
	for _, forbidden := range []string{"api_key", "apikey", "GATEWAY_UPSTREAM_AUTH", "secret"} {
		assert.NotContains(t, string(data), forbidden)
	}
}

// TestSwitchReadyMatrix: the stage-1→2 gate, evidence by evidence.
// The load-bearing case is the last pair: a converge PREDATING the
// CLI's env-file write proves nothing about the running gateway (the
// write needs a later converge to be picked up), while an
// operator-prepared file (no write on record) switches on the first
// signal.
func TestSwitchReadyMatrix(t *testing.T) {
	cases := []struct {
		name  string
		state GenesisState
		env   bool
		want  bool
	}{
		{"already switched", GenesisState{Stage: StageGateway, ConvergedAt: stamp(t0)}, true, false},
		{"no converge yet", GenesisState{Stage: StageDirect}, true, false},
		{"env mismatch", GenesisState{Stage: StageDirect, ConvergedAt: stamp(t0)}, false, false},
		{
			"operator prepared file switches on first signal",
			GenesisState{Stage: StageDirect, ConvergedAt: stamp(t0)},
			true,
			true,
		},
		{
			"cli written file waits for a later converge",
			GenesisState{Stage: StageDirect, ConvergedAt: stamp(t0), EnvWrittenAt: stamp(t1)},
			true,
			false,
		},
		{
			"converge after the write completes",
			GenesisState{Stage: StageDirect, ConvergedAt: stamp(t2), EnvWrittenAt: stamp(t1)},
			true,
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.state.SwitchReady(tc.env))
		})
	}
}

func stamp(now time.Time) string { return now.UTC().Format(time.RFC3339) }

// TestGatewayEnvRoundTrip: the rendered env file is exactly what the
// gateway binary consumes, and the matcher tolerates operator edits —
// comments, blank lines, unrelated entries, reordering.
func TestGatewayEnvRoundTrip(t *testing.T) {
	endpoint, key := "https://genesis.example.com/v1", "genesis-key"
	content := RenderGatewayEnv(endpoint, key)
	assert.True(t, GatewayEnvMatches(content, endpoint, key))
	assert.False(t, GatewayEnvMatches(content, "https://other.example.com/v1", key))
	assert.False(t, GatewayEnvMatches(content, endpoint, "wrong"))

	handEdited := "# prepared by the operator\n\nUNRELATED=value\n" +
		GatewayUpstreamAuthEnv + "=" + key + "\n" +
		GatewayUpstreamEnv + "=" + endpoint + "\n"
	assert.True(t, GatewayEnvMatches(handEdited, endpoint, key), "operator edits must not matter")

	assert.False(t, GatewayEnvMatches("", endpoint, key))
}

// TestGatewayEnvRenderShape: exactly two KEY=value lines, docker
// env-file format, no trailing whitespace games.
func TestGatewayEnvRenderShape(t *testing.T) {
	content := RenderGatewayEnv("https://e.example.com/v1", "k")
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, GatewayUpstreamEnv+"=https://e.example.com/v1", lines[0])
	assert.Equal(t, GatewayUpstreamAuthEnv+"=k", lines[1])
}
