// SPDX-License-Identifier: Apache-2.0

package factscmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
	joinadapter "github.com/kikakkz/looming/platform/go/joinadapter"
)

var mergeNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func boolPtr(b bool) *bool { return &b }

func observedHost(id, address string, caps *hostdomain.Capabilities) joinadapter.ObservedHost {
	return joinadapter.ObservedHost{ID: id, Address: address, Labels: []string{"role=engine"}, Capabilities: caps}
}

func fullObserved() *hostdomain.Capabilities {
	return &hostdomain.Capabilities{
		Hardware:    hostdomain.HardwareCapabilities{CPUCores: 8, MemoryMB: 32768, DiskGB: 457, Arch: "x86_64"},
		Network:     hostdomain.NetworkCapabilities{Egress: boolPtr(true), LatenciesMS: map[string]int{"node-2": 3}},
		CollectedAt: mergeNow,
	}
}

// TestMatchHostsPrefersIDThenAddress: identity wins; address is the
// fallback for server-minted ids; leftovers are reported, every
// observed host consumed at most once.
func TestMatchHostsPrefersIDThenAddress(t *testing.T) {
	declared := []config.Host{
		{ID: "node1", Address: "10.0.0.11"},
		{ID: "node2", Address: "10.0.0.12"},
	}
	observed := []joinadapter.ObservedHost{
		observedHost("node1", "10.0.0.99", nil),         // id match wins over the stale address
		observedHost("host-deadbeef", "10.0.0.12", nil), // address fallback
		observedHost("host-cafe", "10.0.0.77", nil),     // nothing matches
	}

	matched, unmatched := matchHosts(declared, observed)
	require.Len(t, matched, 2)
	assert.Equal(t, "node1", matched[0].declared.ID)
	assert.Equal(t, "node1", matched[0].observed.ID)
	assert.Equal(t, "node2", matched[1].declared.ID)
	assert.Equal(t, "host-deadbeef", matched[1].observed.ID)
	require.Len(t, unmatched, 1)
	assert.Equal(t, "host-cafe", unmatched[0].ID)

	// Two observed hosts can never claim the same declared host.
	declared = []config.Host{{ID: "node1", Address: "10.0.0.11"}}
	observed = []joinadapter.ObservedHost{
		observedHost("node1", "10.0.0.11", nil),
		observedHost("host-xyz", "10.0.0.11", nil),
	}
	matched, unmatched = matchHosts(declared, observed)
	assert.Len(t, matched, 1)
	assert.Len(t, unmatched, 1)
}

// TestMergeFactsObservedWins covers the override matrix: observed
// values replace declared ones; zone and operator latencies survive
// where observation says nothing.
func TestMergeFactsObservedWins(t *testing.T) {
	declared := &config.Capabilities{
		Hardware: config.Hardware{CPUCores: 4, MemoryMB: 16384, DiskGB: 200, Arch: config.ArchX8664},
		Network:  config.Network{Zone: config.ZoneLAN, Egress: boolPtr(false), LatenciesMS: map[string]int{"old": 9}},
	}

	merged, changes := mergeFacts(declared, fullObserved())
	require.NotNil(t, merged)
	assert.Equal(t, 8, merged.Hardware.CPUCores)
	assert.Equal(t, 32768, merged.Hardware.MemoryMB)
	assert.Equal(t, 457, merged.Hardware.DiskGB)
	assert.Equal(t, config.ArchX8664, merged.Hardware.Arch)
	assert.Equal(t, config.ZoneLAN, merged.Network.Zone, "the zone is operator-declared and never observed")
	require.NotNil(t, merged.Network.Egress)
	assert.True(t, *merged.Network.Egress)
	assert.Equal(t, map[string]int{"node-2": 3}, merged.Network.LatenciesMS)

	require.Len(t, changes, 5)
	assert.Contains(t, changes, fieldChange{field: "cpu_cores", from: "4", to: "8"})
	assert.Contains(t, changes, fieldChange{field: "memory_mb", from: "16384", to: "32768"})
	assert.Contains(t, changes, fieldChange{field: "disk_gb", from: "200", to: "457"})
	assert.Contains(t, changes, fieldChange{field: "egress", from: "false", to: "true"})
	assert.Contains(t, changes, fieldChange{field: "latencies_ms", from: "{old:9}", to: "{node-2:3}"})
}

// TestMergeFactsProtectsOperatorSemantics: a declared zone survives a
// from-scratch block; declared labels are not part of the merge at
// all; unobserved fields keep their declared values.
func TestMergeFactsProtectsOperatorSemantics(t *testing.T) {
	declared := &config.Capabilities{
		Network: config.Network{Zone: config.ZoneCloud, Egress: boolPtr(true)},
	}
	observed := &hostdomain.Capabilities{
		Hardware:    hostdomain.HardwareCapabilities{CPUCores: 2},
		Network:     hostdomain.NetworkCapabilities{Egress: boolPtr(true)}, // same value: no change line
		CollectedAt: mergeNow,
	}
	merged, changes := mergeFacts(declared, observed)
	require.NotNil(t, merged)
	assert.Equal(t, config.ZoneCloud, merged.Network.Zone)
	assert.Equal(t, 2, merged.Hardware.CPUCores)
	require.Len(t, changes, 1)
	assert.Equal(t, "cpu_cores", changes[0].field)

	// nil observed keeps the declared block untouched, nil declared.
	assert.Nil(t, func() *config.Capabilities {
		m, _ := mergeFacts(nil, nil)
		return m
	}())
	same, changes := mergeFacts(declared, nil)
	assert.Same(t, declared, same)
	assert.Nil(t, changes)
}

// TestMergeFactsBuildsFromScratch: a host with no declared block gains
// one from pure observation.
func TestMergeFactsBuildsFromScratch(t *testing.T) {
	merged, changes := mergeFacts(nil, fullObserved())
	require.NotNil(t, merged)
	assert.Equal(t, 8, merged.Hardware.CPUCores)
	assert.Empty(t, merged.Network.Zone)
	require.Len(t, changes, 6)
	for _, c := range changes {
		assert.Equal(t, "unset", c.from, "a from-scratch merge reports every field as new: %+v", c)
	}

	// An observation that agrees with the declared block changes
	// nothing and allocates nothing.
	declared := &config.Capabilities{
		Hardware: config.Hardware{CPUCores: 8, MemoryMB: 32768, DiskGB: 457, Arch: config.ArchX8664},
		Network:  config.Network{Egress: boolPtr(true), LatenciesMS: map[string]int{"node-2": 3}},
	}
	merged, changes = mergeFacts(declared, fullObserved())
	assert.Equal(t, declared, merged)
	assert.Empty(t, changes)

	// An empty observation on an undeclared host stays undeclared.
	merged, changes = mergeFacts(nil, &hostdomain.Capabilities{CollectedAt: mergeNow})
	assert.Nil(t, merged)
	assert.Empty(t, changes)
}

func TestRenderLatencies(t *testing.T) {
	assert.Equal(t, "unset", renderLatencies(nil))
	assert.Equal(t, "{}", renderLatencies(map[string]int{}))
	assert.Equal(t, "{a:1 b:2}", renderLatencies(map[string]int{"b": 2, "a": 1}))
}
