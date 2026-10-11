// SPDX-License-Identifier: Apache-2.0

package factscmd

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
)

// TestSpliceRewritesOnlyTouchedHosts: the merge touches node1 only —
// node2's block (with its comment), the bare host, and the placements
// section survive; node1's block is fully replaced by the merged
// values.
func TestSpliceRewritesOnlyTouchedHosts(t *testing.T) {
	document := []byte(`version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.11"}
hosts:
  - id: node1
    address: 10.0.0.11
    capabilities:
      hardware: {cpu_cores: 4, memory_mb: 16384, disk_gb: 200, arch: x86_64}
      network: {zone: cloud, egress: false}
  - id: node2
    address: 10.0.0.12
    # the operator's note must survive an untouched host
    capabilities:
      hardware: {cpu_cores: 1, memory_mb: 2048}
      network: {zone: lan}
  - id: bare
    address: 10.0.0.13
placements:
  - {component: gateway-front, host: node1, ports: {http: 8080}}
`)
	hosts := []config.Host{
		{ID: "node1", Address: "10.0.0.11"},
		{ID: "node2", Address: "10.0.0.12"},
		{ID: "bare", Address: "10.0.0.13"},
	}
	merged := map[string]*config.Capabilities{
		"node1": {
			Hardware: config.Hardware{CPUCores: 8, MemoryMB: 32768, DiskGB: 457, Arch: config.ArchX8664},
			Network:  config.Network{Zone: config.ZoneCloud, Egress: boolPtr(true)},
		},
	}

	out, err := spliceCapabilities(document, hosts, merged)
	require.NoError(t, err)

	// The result must load through the real validator.
	path := writeTempTopology(t, out)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	byID := map[string]config.Host{}
	for _, h := range cfg.Hosts {
		byID[h.ID] = h
	}
	require.NotNil(t, byID["node1"].Capabilities)
	assert.Equal(t, 8, byID["node1"].Capabilities.Hardware.CPUCores)
	assert.Equal(t, 32768, byID["node1"].Capabilities.Hardware.MemoryMB)
	require.NotNil(t, byID["node1"].Capabilities.Network.Egress)
	assert.True(t, *byID["node1"].Capabilities.Network.Egress)
	assert.Equal(t, config.ZoneCloud, byID["node1"].Capabilities.Network.Zone)

	assert.Equal(t, 2048, byID["node2"].Capabilities.Hardware.MemoryMB, "untouched hosts keep their block")
	assert.Nil(t, byID["bare"].Capabilities)

	text := string(out)
	assert.Contains(t, text, "# the operator's note must survive an untouched host")
	assert.Contains(t, text, "- {component: gateway-front, host: node1, ports: {http: 8080}}")
	assert.NotContains(t, text, "cpu_cores: 4")
}

// TestSpliceAppendsBlockToBareHost: a matched host without a
// capabilities key gains one at the end of its mapping.
func TestSpliceAppendsBlockToBareHost(t *testing.T) {
	document := []byte(`version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.11"}
hosts:
  - id: bare
    address: 10.0.0.13
`)
	hosts := []config.Host{{ID: "bare", Address: "10.0.0.13"}}
	merged := map[string]*config.Capabilities{
		"bare": {
			Hardware: config.Hardware{CPUCores: 2, Arch: config.ArchARM64},
			Network:  config.Network{Egress: boolPtr(false)},
		},
	}

	out, err := spliceCapabilities(document, hosts, merged)
	require.NoError(t, err)
	cfg, err := config.Load(writeTempTopology(t, out))
	require.NoError(t, err)
	require.Len(t, cfg.Hosts, 1)
	require.NotNil(t, cfg.Hosts[0].Capabilities)
	assert.Equal(t, 2, cfg.Hosts[0].Capabilities.Hardware.CPUCores)
	require.NotNil(t, cfg.Hosts[0].Capabilities.Network.Egress)
	assert.False(t, *cfg.Hosts[0].Capabilities.Network.Egress)
}

// TestSpliceErrors: malformed documents and merged hosts missing from
// the file fail loudly instead of writing a half-merged file.
func TestSpliceErrors(t *testing.T) {
	hosts := []config.Host{{ID: "node1", Address: "10.0.0.11"}}

	_, err := spliceCapabilities([]byte("not yaml: ["), hosts, nil)
	require.Error(t, err)

	_, err = spliceCapabilities([]byte("- just\n- a\n- list\n"), hosts, nil)
	require.Error(t, err)

	_, err = spliceCapabilities([]byte("version: 1\nhosts: []\n"), hosts,
		map[string]*config.Capabilities{"node1": {}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in the document's hosts section")
}

// TestRenderCapabilitiesOmitsEmptyBlocks: a partial observation
// renders a partial block — no wall of zeros, no empty sections.
func TestRenderCapabilitiesOmitsEmptyBlocks(t *testing.T) {
	out := renderCapabilities(&config.Capabilities{
		Hardware: config.Hardware{CPUCores: 2},
		Network:  config.Network{Zone: config.ZoneLAN},
	})
	require.NotNil(t, out.Hardware)
	assert.Equal(t, 2, out.Hardware.CPUCores)
	assert.Zero(t, out.Hardware.MemoryMB)
	require.NotNil(t, out.Network)
	assert.Equal(t, config.ZoneLAN, out.Network.Zone)
	assert.Nil(t, out.Network.Egress)

	empty := renderCapabilities(&config.Capabilities{})
	assert.Nil(t, empty.Hardware)
	assert.Nil(t, empty.Network)
}

func writeTempTopology(t *testing.T, content []byte) string {
	t.Helper()
	path := t.TempDir() + "/topology.yaml"
	require.NoError(t, os.WriteFile(path, content, 0o600))
	return path
}
