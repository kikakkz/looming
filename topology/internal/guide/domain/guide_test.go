// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/guide/domain"
)

var fixedNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// prodFacts is a render input with every URL fact present — the shape
// the app layer derives from a fully-placed phase-1 topology.
func prodFacts() domain.Facts {
	return domain.Facts{
		Revision:       7,
		AccessPublic:   true,
		ClusterName:    "prod cluster",
		CLIDownloadURL: "https://releases.example.com/looming",
		IdentityURL:    "http://10.0.0.12:8081",
		GatewayURL:     "http://10.0.0.11:8080",
	}
}

func TestRenderBuildsSnapshotFromFacts(t *testing.T) {
	g := domain.Render(prodFacts(), fixedNow)

	assert.Equal(t, domain.SingletonID, g.ID)
	assert.Equal(t, int64(7), g.RenderedRev)
	assert.Equal(t, fixedNow, g.RenderedAt)

	s := g.Snapshot
	assert.Equal(t, "prod cluster", s.ClusterName)
	assert.True(t, s.AccessPublic)
	assert.Equal(t, "https://releases.example.com/looming", s.CLIDownloadURL)
	assert.Equal(t, "http://10.0.0.12:8081", s.IdentityURL)
	assert.Equal(t, "http://10.0.0.11:8080", s.GatewayURL)

	require.Len(t, s.Steps, 4)
	assert.Contains(t, s.Steps[0], s.CLIDownloadURL)
	assert.Contains(t, s.Steps[1], s.IdentityURL)
	assert.NotEmpty(t, s.Steps[2])
	assert.Contains(t, s.Steps[3], s.GatewayURL)

	assert.NotEmpty(t, s.RegisterHint)
	assert.Contains(t, s.RegisterHint, "admin")
}

func TestRenderDegradesMissingURLs(t *testing.T) {
	f := prodFacts()
	f.IdentityURL = ""
	f.GatewayURL = ""
	g := domain.Render(f, fixedNow)

	require.Len(t, g.Snapshot.Steps, 4)
	assert.NotContains(t, g.Snapshot.Steps[1], "http://")
	assert.NotContains(t, g.Snapshot.Steps[3], "http://")
}

func TestSnapshotJSONShapeCarriesNoCredentials(t *testing.T) {
	// The zero-credential invariant, asserted structurally: the wire
	// shape is exactly this key set, so no field can smuggle secret
	// material to the public page (topology-l1 §5 Guide row).
	raw, err := json.Marshal(domain.Render(prodFacts(), fixedNow).Snapshot)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	wantKeys := []string{
		"cluster_name", "access_public", "cli_download_url",
		"identity_url", "gateway_url", "steps", "register_hint",
	}
	require.Len(t, decoded, len(wantKeys), "snapshot shape is fixed — adding a field is a contract change")
	for _, k := range wantKeys {
		assert.Contains(t, decoded, k)
	}

	// Defence in depth: no value anywhere in the payload carries
	// credential vocabulary (the fixed steps may name "an API key",
	// never key *material*).
	for _, banned := range []string{"password", "credential", "secret", "bearer", "token:"} {
		assert.NotContains(t, string(raw), banned, "snapshot must never carry %q", banned)
	}
}

func TestFactsCarryNoSecretInputs(t *testing.T) {
	// The render input is structurally incapable of receiving
	// credentials: its string fields are exactly these (a test over the
	// type's vocabulary, so a future secret-bearing field lands here).
	f := prodFacts()
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	for k := range decoded {
		assert.False(t, strings.Contains(strings.ToLower(k), "token") ||
			strings.Contains(strings.ToLower(k), "secret") ||
			strings.Contains(strings.ToLower(k), "password"), "facts field %q smells like a secret channel", k)
	}
}

func TestCurrentForMatchesEveryFact(t *testing.T) {
	f := prodFacts()
	g := domain.Render(f, fixedNow)

	assert.True(t, g.CurrentFor(f))

	cases := []struct {
		name   string
		mutate func(*domain.Facts)
	}{
		{"revision", func(f *domain.Facts) { f.Revision++ }},
		{"cluster name", func(f *domain.Facts) { f.ClusterName = "other" }},
		{"access", func(f *domain.Facts) { f.AccessPublic = !f.AccessPublic }},
		{"cli url", func(f *domain.Facts) { f.CLIDownloadURL = "https://other.example.com" }},
		{"identity url", func(f *domain.Facts) { f.IdentityURL = "http://10.0.0.99:1" }},
		{"gateway url", func(f *domain.Facts) { f.GatewayURL = "http://10.0.0.99:2" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := prodFacts()
			tc.mutate(&changed)
			assert.False(t, g.CurrentFor(changed),
				"a changed %s must re-render even at the same revision (config facts do not bump it)", tc.name)
		})
	}
}
