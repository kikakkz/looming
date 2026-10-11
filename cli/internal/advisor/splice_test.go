// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
)

const spliceFixture = `version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts:
  - id: gw-1
    address: 10.0.0.11
  - id: app-1
    address: 10.0.0.12
genesis: {endpoint: "https://genesis.example.com/v1"}
`

// TestSplicePlacementsReplacesSection: the placements block is
// replaced; every other section round-trips untouched (the yaml.Node
// edit keeps styles — the diff shows placements and nothing else).
func TestSplicePlacementsReplacesSection(t *testing.T) {
	document := []byte(spliceFixture + `placements:
  - component: gateway-front
    host: gw-1
    ports: {http: 8080}
`)
	spliced, err := splicePlacements(document, []config.Placement{
		{Component: "gateway-front", Host: "app-1", Ports: map[string]int{"http": 8080}},
		{Component: "identityd", Host: "gw-1", Ports: map[string]int{"http": 1024}},
	}, []config.Placement{
		{Component: "gateway-front", Host: "gw-1", Ports: map[string]int{"http": 8080}},
	})
	require.NoError(t, err)
	out := string(spliced)

	assert.Contains(t, out, "access: {mode: public, transport: direct, endpoint: \"10.0.0.10\"}", "flow styles survive")
	assert.Contains(t, out, "genesis: {endpoint: \"https://genesis.example.com/v1\"}", "sections after placements survive")
	assert.Contains(t, out, "- component: gateway-front\n    host: app-1")
	assert.Contains(t, out, "- component: identityd\n    host: gw-1")
	assert.NotContains(t, out, "host: gw-1\n    ports: {http: 8080}", "the old entry is gone")

	// The spliced document must load through the real validator.
	path := filepath.Join(t.TempDir(), "topology.yaml")
	require.NoError(t, os.WriteFile(path, spliced, 0o600))
	_, err = config.Load(path)
	require.NoError(t, err, "the splice must produce a valid topology")
}

// TestSplicePlacementsCreatesSection: a file without a placements
// section gains one at the end.
func TestSplicePlacementsCreatesSection(t *testing.T) {
	spliced, err := splicePlacements([]byte(spliceFixture), []config.Placement{
		{Component: "identityd", Host: "app-1", Ports: map[string]int{"http": 1024}},
	}, nil)
	require.NoError(t, err)
	out := string(spliced)
	assert.True(t, strings.HasSuffix(strings.TrimSpace(out), "ports:\n      http: 1024"), "created section lands at the end:\n%s", out)

	path := filepath.Join(t.TempDir(), "topology.yaml")
	require.NoError(t, os.WriteFile(path, spliced, 0o600))
	_, err = config.Load(path)
	require.NoError(t, err)
}

// TestSplicePlacementsCarriesExistingEntryDetails: a moved component
// keeps its operator wiring — config entries, extra_hosts, env_file —
// only the host changes.
func TestSplicePlacementsCarriesExistingEntryDetails(t *testing.T) {
	document := []byte(spliceFixture + `placements:
  - component: gateway-front
    host: gw-1
    ports: {http: 8080}
    config: {upstream: "http://10.0.0.13:4000"}
    env_file: /etc/looming/gateway.env
`)
	spliced, err := splicePlacements(document, []config.Placement{
		{Component: "gateway-front", Host: "app-1", Ports: map[string]int{"http": 8080},
			Config: map[string]string{"upstream": "http://10.0.0.13:4000"}, EnvFile: "/etc/looming/gateway.env"},
	}, []config.Placement{
		{Component: "gateway-front", Host: "gw-1", Ports: map[string]int{"http": 8080},
			Config: map[string]string{"upstream": "http://10.0.0.13:4000"}, EnvFile: "/etc/looming/gateway.env"},
	})
	require.NoError(t, err)
	out := string(spliced)
	assert.Contains(t, out, "host: app-1")
	assert.Contains(t, out, "config:")
	assert.Contains(t, out, "upstream: http://10.0.0.13:4000")
	assert.Contains(t, out, "env_file: /etc/looming/gateway.env")
}

func TestSplicePlacementsRejectsNonMapping(t *testing.T) {
	_, err := splicePlacements([]byte("- just\n- a\n- list\n"), nil, nil)
	require.Error(t, err)
}
