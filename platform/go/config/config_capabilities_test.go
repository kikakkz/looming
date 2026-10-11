// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
)

// TestLoadCapabilitiesRoundTrips: the facts block parses field for
// field, including the egress pointer's three-state contract (absent,
// declared true, declared false).
func TestLoadCapabilitiesRoundTrips(t *testing.T) {
	cfg, err := load(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts:
  - id: gw-1
    address: 10.0.0.11
    capabilities:
      hardware: {cpu_cores: 8, memory_mb: 16384, disk_gb: 200, arch: arm64}
      network:
        zone: cloud
        egress: true
        latencies_ms: {app-1: 3}
  - id: app-1
    address: 10.0.0.12
    capabilities:
      hardware: {cpu_cores: 4, memory_mb: 8192}
      network: {zone: lan, egress: false}
  - id: bare
    address: 10.0.0.13
placements: [{component: gateway-front, host: gw-1, ports: {http: 8080}}]
`)
	require.NoError(t, err)
	require.Len(t, cfg.Hosts, 3)

	gw := cfg.Hosts[0]
	require.NotNil(t, gw.Capabilities)
	assert.Equal(t, config.Hardware{CPUCores: 8, MemoryMB: 16384, DiskGB: 200, Arch: config.ArchARM64}, gw.Capabilities.Hardware)
	require.NotNil(t, gw.Capabilities.Network.Egress)
	assert.True(t, *gw.Capabilities.Network.Egress)
	assert.Equal(t, map[string]int{"app-1": 3}, gw.Capabilities.Network.LatenciesMS)

	app := cfg.Hosts[1]
	require.NotNil(t, app.Capabilities)
	assert.Equal(t, "", app.Capabilities.Hardware.Arch, "absent scalar fact stays zero-valued")
	require.NotNil(t, app.Capabilities.Network.Egress)
	assert.False(t, *app.Capabilities.Network.Egress, "declared false survives the parse")

	// Phase-1 files: a host without the block stays nil.
	assert.Nil(t, cfg.Hosts[2].Capabilities)
}

// TestLoadCapabilitiesAbsentEgressStaysNil: the pointer exists to
// distinguish "not declared" from "declared false" — the absent case
// must parse to nil.
func TestLoadCapabilitiesAbsentEgressStaysNil(t *testing.T) {
	cfg, err := load(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts:
  - id: gw-1
    address: 10.0.0.11
    capabilities:
      hardware: {cpu_cores: 8}
      network: {zone: lan}
placements: [{component: gateway-front, host: gw-1, ports: {http: 8080}}]
`)
	require.NoError(t, err)
	assert.Nil(t, cfg.Hosts[0].Capabilities.Network.Egress)
}

func TestLoadCapabilitiesValidation(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"negative cpu", "hardware: {cpu_cores: -1}"},
		{"negative memory", "hardware: {memory_mb: -1}"},
		{"negative disk", "hardware: {disk_gb: -1}"},
		{"unknown arch", "hardware: {arch: riscv}"},
		{"unknown zone", "network: {zone: wan}"},
		{"negative latency", "network: {latencies_ms: {app-1: -3}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts:
  - id: gw-1
    address: 10.0.0.11
    capabilities:
      `+tc.yaml+`
placements: [{component: gateway-front, host: gw-1, ports: {http: 8080}}]
`)
			require.ErrorIs(t, err, config.ErrInvalidHost)
		})
	}
}

func TestLoadCapabilitiesAcceptsVocabularyEdges(t *testing.T) {
	cfg, err := load(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts:
  - id: gw-1
    address: 10.0.0.11
    capabilities:
      hardware: {arch: x86_64}
      network: {zone: lan}
placements: [{component: gateway-front, host: gw-1, ports: {http: 8080}}]
`)
	require.NoError(t, err)
	assert.Equal(t, config.ArchX8664, cfg.Hosts[0].Capabilities.Hardware.Arch)
	assert.Equal(t, config.ZoneLAN, cfg.Hosts[0].Capabilities.Network.Zone)
}

func TestLoadOldFileWithoutCapabilitiesStillValid(t *testing.T) {
	cfg, err := load(t, validYAML)
	require.NoError(t, err)
	for _, h := range cfg.Hosts {
		assert.Nil(t, h.Capabilities)
	}
}
