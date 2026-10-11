// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
)

// proposalFixtureInput is the smallest honest input: two hosts, one
// profile, one feasible and one infeasible pair.
func proposalFixtureInput() ProposalInput {
	egress := true
	hosts := []config.Host{
		{ID: "gw-1", Capabilities: &config.Capabilities{Network: config.Network{Zone: config.ZoneCloud, Egress: &egress}, Hardware: config.Hardware{CPUCores: 8, MemoryMB: 16384, DiskGB: 200, Arch: config.ArchX8664}}},
		{ID: "app-1", Capabilities: &config.Capabilities{Network: config.Network{Zone: config.ZoneLAN, Egress: new(bool)}, Hardware: config.Hardware{CPUCores: 1, MemoryMB: 2048, DiskGB: 20, Arch: config.ArchARM64}}},
	}
	profiles := map[string]Profile{
		"identityd": {Name: "identityd", Hard: Hard{MinMemoryMB: 4096, MinCPUCores: 2}, Soft: Soft{PreferredZone: config.ZoneLAN}},
	}
	verdicts := Evaluate(hosts, profiles)
	return ProposalInput{
		Verdicts:   verdicts,
		Profiles:   profiles,
		Hosts:      hosts,
		Preference: "node app-1 runs a database, avoid it",
	}
}

// TestBuildProposalMessagesCarriesFeasibleSetSoftAndPreference: the
// document is the model's entire view — feasible pairs with context,
// the soft sections, the session preference — and it never quotes
// infeasible pairs or hard-rule internals.
func TestBuildProposalMessagesCarriesFeasibleSetSoftAndPreference(t *testing.T) {
	in := proposalFixtureInput()
	messages := BuildProposalMessages(in)
	require.Len(t, messages, 2)
	assert.Equal(t, "system", messages[0].Role)
	assert.Contains(t, messages[0].Content, "JSON document")

	user := messages[1].Content
	assert.Contains(t, user, "identityd on gw-1")
	assert.Contains(t, user, "zone: cloud")
	assert.Contains(t, user, `preferred_zone="lan"`)
	assert.Contains(t, user, "node app-1 runs a database, avoid it")
	assert.NotContains(t, user, "identityd on app-1", "infeasible pairs never reach the model")
	assert.NotContains(t, user, "INFEASIBLE")

	again := BuildProposalMessages(in)
	assert.Equal(t, messages, again, "the document is deterministic")
}

func TestBuildProposalMessagesEmptyFeasibleSet(t *testing.T) {
	in := proposalFixtureInput()
	in.Verdicts = nil
	user := BuildProposalMessages(in)[1].Content
	assert.Contains(t, user, "(none — every pair failed a hard rule)")
}

// TestParseProposal: JSON-only answers, prose-wrapped answers, and the
// failure matrix (no document, malformed JSON) — the recorded-response
// shapes the fake will serve.
func TestParseProposal(t *testing.T) {
	valid := `{"placements":[{"component":"identityd","host":"gw-1","reason":"LAN reachability"}],"risks":["single point of failure"]}`

	t.Run("json only", func(t *testing.T) {
		p, err := ParseProposal(valid)
		require.NoError(t, err)
		require.Len(t, p.Placements, 1)
		assert.Equal(t, "identityd", p.Placements[0].Component)
		assert.Equal(t, "LAN reachability", p.Placements[0].Reason)
		assert.Equal(t, []string{"single point of failure"}, p.Risks)
	})

	t.Run("prose wrapped", func(t *testing.T) {
		p, err := ParseProposal("Here you go:\n```json\n" + valid + "\n```\nHope this helps!")
		require.NoError(t, err)
		assert.Len(t, p.Placements, 1)
	})

	t.Run("no document", func(t *testing.T) {
		_, err := ParseProposal("I cannot propose anything.")
		require.Error(t, err)
		assert.True(t, errors.Is(err, errProposal))
	})

	t.Run("malformed json", func(t *testing.T) {
		_, err := ParseProposal(`{"placements": [`)
		require.Error(t, err)
		assert.True(t, errors.Is(err, errProposal))
	})
}

// TestCheckProposalMatrix: the guardrail's rule vocabulary, one case
// per rejection reason plus the all-pass path.
func TestCheckProposalMatrix(t *testing.T) {
	in := proposalFixtureInput()
	valid := ProposedPlacement{Component: "identityd", Host: "gw-1", Reason: "LAN reachability"}

	t.Run("accepts the feasible pair", func(t *testing.T) {
		accepted, violations := CheckProposal(&Proposal{Placements: []ProposedPlacement{valid}}, in)
		assert.Empty(t, violations)
		assert.Equal(t, []ProposedPlacement{valid}, accepted)
	})

	t.Run("rejects unknown component", func(t *testing.T) {
		bad := ProposedPlacement{Component: "nosuch", Host: "gw-1"}
		_, violations := CheckProposal(&Proposal{Placements: []ProposedPlacement{bad}}, in)
		require.Len(t, violations, 1)
		assert.Contains(t, violations[0].Problem, `unknown component "nosuch"`)
	})

	t.Run("rejects unknown host", func(t *testing.T) {
		bad := ProposedPlacement{Component: "identityd", Host: "nowhere"}
		_, violations := CheckProposal(&Proposal{Placements: []ProposedPlacement{bad}}, in)
		require.Len(t, violations, 1)
		assert.Contains(t, violations[0].Problem, `unknown host "nowhere"`)
	})

	t.Run("rejects infeasible pair with the rule quoted", func(t *testing.T) {
		bad := ProposedPlacement{Component: "identityd", Host: "app-1"}
		accepted, violations := CheckProposal(&Proposal{Placements: []ProposedPlacement{bad, valid}}, in)
		assert.Equal(t, []ProposedPlacement{valid}, accepted, "valid placements still pass")
		require.Len(t, violations, 1)
		assert.Contains(t, violations[0].Problem, "infeasible pair")
		assert.Contains(t, violations[0].Problem, "resource-floor")
	})

	t.Run("rejects duplicate component", func(t *testing.T) {
		dup := ProposedPlacement{Component: "identityd", Host: "gw-1", Reason: "again"}
		_, violations := CheckProposal(&Proposal{Placements: []ProposedPlacement{valid, dup}}, in)
		require.Len(t, violations, 1)
		assert.Contains(t, violations[0].Problem, "more than once")
	})
}

// scriptedClient serves recorded responses in order — the CI fake the
// test pyramid promises; a recorder captures the conversation for
// regeneration-feedback assertions.
type scriptedClient struct {
	responses []string
	served    int
	requests  []ChatRequest
}

func (c *scriptedClient) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	if c.served >= len(c.responses) {
		return nil, errors.New("scriptedClient: out of recorded responses")
	}
	c.requests = append(c.requests, req)
	content := c.responses[c.served]
	c.served++
	return &ChatResponse{Choices: []struct {
		Message ChatMessage `json:"message"`
	}{{Message: ChatMessage{Role: "assistant", Content: content}}}}, nil
}

func proposalJSON(component, host string) string {
	return `{"placements":[{"component":"` + component + `","host":"` + host + `","reason":"ok"}],"risks":[]}`
}

// TestProposeWithGuardrailAcceptsFirstTry: one clean proposal, one
// attempt, no rejections.
func TestProposeWithGuardrailAcceptsFirstTry(t *testing.T) {
	in := proposalFixtureInput()
	client := &scriptedClient{responses: []string{proposalJSON("identityd", "gw-1")}}

	result, err := ProposeWithGuardrail(context.Background(), client, in)
	require.NoError(t, err)
	assert.False(t, result.Degraded)
	assert.Equal(t, 1, result.Attempts)
	assert.Empty(t, result.Rejections)
	require.NotNil(t, result.Proposal)
	assert.Len(t, result.Proposal.Placements, 1)
}

// TestProposeWithGuardrailRegeneratesOnViolation: the fake proposes an
// infeasible pair first; the rejection names the violation, the
// feedback reaches the model, and the second attempt lands.
func TestProposeWithGuardrailRegeneratesOnViolation(t *testing.T) {
	in := proposalFixtureInput()
	client := &scriptedClient{responses: []string{
		proposalJSON("identityd", "app-1"),
		proposalJSON("identityd", "gw-1"),
	}}

	result, err := ProposeWithGuardrail(context.Background(), client, in)
	require.NoError(t, err)
	assert.False(t, result.Degraded)
	assert.Equal(t, 2, result.Attempts)
	require.Len(t, result.Rejections, 1)
	assert.Contains(t, result.Rejections[0], "infeasible pair")

	// The regeneration feedback carries the violation back to the model.
	require.Len(t, client.requests, 2)
	second := client.requests[1]
	require.GreaterOrEqual(t, len(second.Messages), 4)
	assert.Contains(t, second.Messages[len(second.Messages)-1].Content, "infeasible pair")
}

// TestProposeWithGuardrailRegeneratesOnUnparseable: a non-JSON answer
// is a rejection, not a crash — the model gets the contract reminder.
func TestProposeWithGuardrailRegeneratesOnUnparseable(t *testing.T) {
	in := proposalFixtureInput()
	client := &scriptedClient{responses: []string{
		"Sure! I'd suggest identityd on gw-1.",
		proposalJSON("identityd", "gw-1"),
	}}

	result, err := ProposeWithGuardrail(context.Background(), client, in)
	require.NoError(t, err)
	assert.False(t, result.Degraded)
	assert.Equal(t, 2, result.Attempts)
	assert.Contains(t, result.Rejections[0], "no JSON document")
}

// TestProposeWithGuardrailDegradesAfterMaxAttempts: three rejected
// attempts and the loop yields Degraded — the caller answers with the
// deterministic table.
func TestProposeWithGuardrailDegradesAfterMaxAttempts(t *testing.T) {
	in := proposalFixtureInput()
	client := &scriptedClient{responses: []string{
		proposalJSON("identityd", "app-1"),
		proposalJSON("identityd", "app-1"),
		proposalJSON("identityd", "app-1"),
	}}

	result, err := ProposeWithGuardrail(context.Background(), client, in)
	require.NoError(t, err)
	assert.True(t, result.Degraded)
	assert.Nil(t, result.Proposal)
	assert.Equal(t, MaxProposalAttempts, result.Attempts)
	assert.Len(t, result.Rejections, MaxProposalAttempts)
}

// TestProposeWithGuardrailSurfacesChannelErrors: a channel failure is
// the caller's error — never a silent degradation.
func TestProposeWithGuardrailSurfacesChannelErrors(t *testing.T) {
	in := proposalFixtureInput()
	broken := &scriptedClient{responses: nil}
	_, err := ProposeWithGuardrail(context.Background(), broken, in)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errModelChannel) || strings.Contains(err.Error(), "scriptedClient"))
}
