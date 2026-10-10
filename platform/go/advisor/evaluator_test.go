// SPDX-License-Identifier: Apache-2.0

package advisor_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/advisor"
	"github.com/kikakkz/looming/platform/go/config"
)

// capableHost is a host declaring every fact a hard rule can need:
// generous hardware, declared egress, and an architecture.
func capableHost(id string) config.Host {
	return config.Host{
		ID:      id,
		Address: "10.0.0.1",
		Capabilities: &config.Capabilities{
			Hardware: config.Hardware{
				CPUCores: 4,
				MemoryMB: 8192,
				DiskGB:   100,
				Arch:     config.ArchX8664,
			},
			Network: config.Network{
				Zone:   config.ZoneCloud,
				Egress: boolPtr(true),
			},
		},
	}
}

// noEgressHost is capableHost with egress explicitly declared false.
func noEgressHost(id string) config.Host {
	h := capableHost(id)
	h.Capabilities.Network.Egress = boolPtr(false)
	return h
}

// noCapHost declares no capabilities at all — every needed fact is a gap.
func noCapHost(id string) config.Host {
	return config.Host{ID: id, Address: "10.0.0.9"}
}

func boolPtr(b bool) *bool { return &b }

// profile returns a kitchen-sink profile exercising every rule.
func fullProfile() advisor.Profile {
	return advisor.Profile{
		Name: "gateway-front",
		Kind: "edge",
		Hard: advisor.Hard{
			MinCPUCores: 2,
			MinMemoryMB: 1024,
			MinDiskGB:   10,
			Arch:        []string{config.ArchX8664, config.ArchARM64},
			NeedsEgress: true,
			Ports:       []string{"http"},
		},
		Soft: advisor.Soft{PreferredZone: "cloud", Spread: "component"},
	}
}

func TestEvaluateFeasiblePairReportsHeadroomAndClaimedPorts(t *testing.T) {
	verdicts := advisor.Evaluate(
		[]config.Host{capableHost("gw-1")},
		map[string]advisor.Profile{"gateway-front": fullProfile()},
	)
	require.Len(t, verdicts, 1)

	v := verdicts[0]
	assert.True(t, v.Feasible)
	assert.Empty(t, v.Violations)
	assert.Empty(t, v.Missing)
	assert.Equal(t, []string{"http"}, v.Ports)
	require.NotNil(t, v.Headroom)
	assert.Equal(t, 8192-1024, v.Headroom.MemoryMB)
	assert.Equal(t, 4-2, v.Headroom.CPUCores)
}

// TestEvaluateViolationMatrix drives every hard rule across its
// hit/miss cases — the full violation matrix advisor-l1 §6 asks for.
func TestEvaluateViolationMatrix(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*advisor.Profile, *config.Host)
		rule     advisor.Rule
		wantFeas bool
		wantMiss []string
	}{
		{
			name:   "cpu floor hit",
			mutate: func(p *advisor.Profile, _ *config.Host) { p.Hard.MinCPUCores = 8 },
			rule:   advisor.RuleResourceFloor,
		},
		{
			name:     "cpu floor miss",
			mutate:   func(p *advisor.Profile, _ *config.Host) { p.Hard.MinCPUCores = 1 },
			wantFeas: true,
		},
		{
			name:   "memory floor hit",
			mutate: func(p *advisor.Profile, _ *config.Host) { p.Hard.MinMemoryMB = 16384 },
			rule:   advisor.RuleResourceFloor,
		},
		{
			name:     "memory floor miss",
			mutate:   func(p *advisor.Profile, _ *config.Host) { p.Hard.MinMemoryMB = 1024 },
			wantFeas: true,
		},
		{
			name:   "disk floor hit",
			mutate: func(p *advisor.Profile, _ *config.Host) { p.Hard.MinDiskGB = 500 },
			rule:   advisor.RuleResourceFloor,
		},
		{
			name:     "disk floor miss",
			mutate:   func(p *advisor.Profile, _ *config.Host) { p.Hard.MinDiskGB = 10 },
			wantFeas: true,
		},
		{
			name:   "egress hit on declared false",
			mutate: func(_ *advisor.Profile, h *config.Host) { h.Capabilities.Network.Egress = boolPtr(false) },
			rule:   advisor.RuleEgress,
		},
		{
			name:     "egress miss on declared true",
			mutate:   func(_ *advisor.Profile, h *config.Host) { h.Capabilities.Network.Egress = boolPtr(true) },
			wantFeas: true,
		},
		{
			name: "arch hit on undeclared-in-list",
			mutate: func(p *advisor.Profile, h *config.Host) {
				p.Hard.Arch = []string{config.ArchARM64}
				h.Capabilities.Hardware.Arch = config.ArchX8664
			},
			rule: advisor.RuleArch,
		},
		{
			name: "arch miss on listed arch",
			mutate: func(p *advisor.Profile, h *config.Host) {
				p.Hard.Arch = []string{config.ArchARM64}
				h.Capabilities.Hardware.Arch = config.ArchARM64
			},
			wantFeas: true,
		},
		{
			name:   "port hit on unknown name",
			mutate: func(p *advisor.Profile, _ *config.Host) { p.Hard.Ports = []string{"metrics"} },
			rule:   advisor.RulePort,
		},
		{
			name:     "port miss on contract listen port",
			mutate:   func(p *advisor.Profile, _ *config.Host) { p.Hard.Ports = []string{"http"} },
			wantFeas: true,
		},
		{
			name: "fact completeness: nil capabilities",
			mutate: func(_ *advisor.Profile, h *config.Host) {
				h.Capabilities = nil
			},
			wantMiss: []string{
				advisor.FactArch, advisor.FactCPUCores, advisor.FactDiskGB, advisor.FactMemoryMB, advisor.FactEgress,
			},
		},
		{
			name: "fact completeness: egress undeclared",
			mutate: func(_ *advisor.Profile, h *config.Host) {
				h.Capabilities.Network.Egress = nil
			},
			wantMiss: []string{advisor.FactEgress},
		},
		{
			name: "fact completeness: arch undeclared",
			mutate: func(_ *advisor.Profile, h *config.Host) {
				h.Capabilities.Hardware.Arch = ""
			},
			wantMiss: []string{advisor.FactArch},
		},
		{
			name: "fact completeness: zero cpu reads as undeclared",
			mutate: func(_ *advisor.Profile, h *config.Host) {
				h.Capabilities.Hardware.CPUCores = 0
			},
			wantMiss: []string{advisor.FactCPUCores},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fullProfile()
			h := capableHost("gw-1")
			tc.mutate(&p, &h)

			verdicts := advisor.Evaluate([]config.Host{h}, map[string]advisor.Profile{p.Name: p})
			require.Len(t, verdicts, 1)
			v := verdicts[0]

			assert.Equal(t, tc.wantFeas, v.Feasible, "feasible: %+v", v)
			if tc.rule != "" {
				require.Len(t, v.Violations, 1, "violations: %+v", v.Violations)
				assert.Equal(t, tc.rule, v.Violations[0].Rule)
				assert.NotEmpty(t, v.Violations[0].Detail)
				assert.Empty(t, v.Missing)
				assert.Nil(t, v.Headroom)
			}
			if tc.wantMiss != nil {
				assert.Empty(t, v.Violations)
				assert.Equal(t, tc.wantMiss, v.Missing)
				assert.False(t, v.Feasible)
			}
		})
	}
}

// TestEvaluateUndeclaredConstraintsStaySilent: a profile declaring no
// constraint imposes none — the host's undeclared facts are gaps only
// when a rule actually needs them.
func TestEvaluateUndeclaredConstraintsStaySilent(t *testing.T) {
	profiles := map[string]advisor.Profile{
		"topologyd": {
			Name: "topologyd",
			Hard: advisor.Hard{Ports: []string{"http"}}, // no floors, no egress, no arch
		},
	}

	// A host with no capabilities at all is still feasible — nothing
	// was needed.
	verdicts := advisor.Evaluate([]config.Host{noCapHost("bare")}, profiles)
	require.Len(t, verdicts, 1)
	assert.True(t, verdicts[0].Feasible)
	assert.Nil(t, verdicts[0].Headroom)

	// A host with partial facts gets headroom for what it declared.
	verdicts = advisor.Evaluate([]config.Host{{
		ID: "half",
		Capabilities: &config.Capabilities{
			Hardware: config.Hardware{CPUCores: 2, MemoryMB: 4096},
		},
	}}, profiles)
	require.Len(t, verdicts, 1)
	assert.True(t, verdicts[0].Feasible)
	require.NotNil(t, verdicts[0].Headroom)
	assert.Equal(t, 4096, verdicts[0].Headroom.MemoryMB)
	assert.Equal(t, 2, verdicts[0].Headroom.CPUCores)
}

func TestEvaluateMultiplePairsDeterministicOrder(t *testing.T) {
	profiles := map[string]advisor.Profile{
		"topologyd": {Name: "topologyd", Hard: advisor.Hard{Ports: []string{"http"}}},
		"identityd": {Name: "identityd", Hard: advisor.Hard{Ports: []string{"http"}}},
	}
	hosts := []config.Host{noEgressHost("a"), capableHost("b")}

	first := advisor.Evaluate(hosts, profiles)
	second := advisor.Evaluate(hosts, profiles)
	require.Equal(t, first, second, "Evaluate must be deterministic")
	require.Len(t, first, 4)

	// Components in sorted-name order, hosts in declaration order.
	assert.Equal(t, "identityd", first[0].Component)
	assert.Equal(t, "a", first[0].Host)
	assert.Equal(t, "identityd", first[1].Component)
	assert.Equal(t, "b", first[1].Host)
	assert.Equal(t, "topologyd", first[2].Component)
	assert.Equal(t, "a", first[2].Host)
	assert.Equal(t, "topologyd", first[3].Component)

	// Claimed ports are sorted for display.
	p := advisor.Profile{Name: "identityd", Hard: advisor.Hard{Ports: []string{"z", "a", "m"}}}
	v := advisor.Evaluate([]config.Host{noCapHost("x")}, map[string]advisor.Profile{"identityd": p})
	require.Len(t, v, 1)
	assert.Equal(t, []string{"a", "m", "z"}, v[0].Ports)
}
