// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/kikakkz/looming/platform/go/hostdomain"
)

var factsNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

const validFacts = `{
	"hardware": {"cpu_cores": 8, "memory_mb": 32768, "disk_gb": 457, "arch": "x86_64"},
	"network":  {"egress": true},
	"collected_at": "2026-10-11T11:59:00Z"
}`

// TestParseCapabilitiesAcceptsTheFullBlock is the happy path: every
// fact populated, egress observed true, collected_at inside the skew
// horizon.
func TestParseCapabilitiesAcceptsTheFullBlock(t *testing.T) {
	caps, err := domain.ParseCapabilities([]byte(validFacts), factsNow)
	require.NoError(t, err)
	assert.Equal(t, 8, caps.Hardware.CPUCores)
	assert.Equal(t, 32768, caps.Hardware.MemoryMB)
	assert.Equal(t, 457, caps.Hardware.DiskGB)
	assert.Equal(t, "x86_64", caps.Hardware.Arch)
	require.NotNil(t, caps.Network.Egress)
	assert.True(t, *caps.Network.Egress)
	assert.True(t, caps.CollectedAt.Equal(time.Date(2026, 10, 11, 11, 59, 0, 0, time.UTC)))
}

// TestParseCapabilitiesAcceptsLegitimateShapes covers the degradations
// the join-side collector legitimately produces: partial facts, an
// empty block (collector ran, observed nothing), egress observed
// false, arm64, and observed latencies keyed by target host.
func TestParseCapabilitiesAcceptsLegitimateShapes(t *testing.T) {
	cases := map[string]string{
		"empty block":        `{"hardware": {}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"hardware only":      `{"hardware": {"cpu_cores": 4}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"egress false":       `{"hardware": {}, "network": {"egress": false}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"arm64":              `{"hardware": {"arch": "arm64"}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"with latencies":     `{"hardware": {}, "network": {"latencies_ms": {"node-2": 3}}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"missing network":    `{"hardware": {"cpu_cores": 2}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"missing hardware":   `{"network": {"egress": true}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"null latencies":     `{"hardware": {}, "network": {"latencies_ms": null}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"arch empty":         `{"hardware": {"arch": ""}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"collected boundary": `{"hardware": {}, "network": {}, "collected_at": "2026-10-12T11:59:59Z"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := domain.ParseCapabilities([]byte(raw), factsNow)
			assert.NoError(t, err)
		})
	}
}

// TestParseCapabilitiesRejectsInvalidBlocks is the strict half of the
// wire contract: unknown keys (anywhere in the subtree), out-of-range
// values, a non-vocabulary arch, negative latencies, a missing or
// malformed collected_at, and stamps beyond the clock-skew horizon.
func TestParseCapabilitiesRejectsInvalidBlocks(t *testing.T) {
	cases := map[string]string{
		"unknown top key":        `{"hardware": {}, "network": {}, "collected_at": "2026-10-11T11:00:00Z", "zone": "cloud"}`,
		"unknown hardware key":   `{"hardware": {"gpus": 2}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"unknown network key":    `{"hardware": {}, "network": {"zone": "lan"}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"negative cpu":           `{"hardware": {"cpu_cores": -1}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"cpu above bound":        `{"hardware": {"cpu_cores": 1025}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"memory above bound":     `{"hardware": {"memory_mb": 16777217}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"negative disk":          `{"hardware": {"disk_gb": -5}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"disk above bound":       `{"hardware": {"disk_gb": 4194305}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"arch not vocabulary":    `{"hardware": {"arch": "riscv64"}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"negative latency":       `{"hardware": {}, "network": {"latencies_ms": {"node-2": -1}}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"latency above bound":    `{"hardware": {}, "network": {"latencies_ms": {"node-2": 1048577}}, "collected_at": "2026-10-11T11:00:00Z"}`,
		"missing collected_at":   `{"hardware": {}, "network": {}}`,
		"malformed collected_at": `{"hardware": {}, "network": {}, "collected_at": "yesterday"}`,
		"collected_at future":    `{"hardware": {}, "network": {}, "collected_at": "2026-10-13T12:00:01Z"}`,
		"trailing garbage":       `{"hardware": {}, "network": {}, "collected_at": "2026-10-11T11:00:00Z"} {}`,
		"not an object":          `["hardware"]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := domain.ParseCapabilities([]byte(raw), factsNow)
			assert.ErrorIs(t, err, domain.ErrInvalidCapabilities)
		})
	}
}

// TestCapabilitiesValidateOnMemoryValues covers the value checks for
// blocks constructed in-process (the bounds matter wherever a
// Capabilities value is built, not only at the wire edge).
func TestCapabilitiesValidateOnMemoryValues(t *testing.T) {
	ok := &domain.Capabilities{CollectedAt: factsNow.Add(-time.Hour)}
	assert.NoError(t, ok.Validate(factsNow))

	noStamp := &domain.Capabilities{}
	assert.ErrorIs(t, noStamp.Validate(factsNow), domain.ErrInvalidCapabilities)

	future := &domain.Capabilities{CollectedAt: factsNow.Add(48 * time.Hour)}
	assert.ErrorIs(t, future.Validate(factsNow), domain.ErrInvalidCapabilities)
}
