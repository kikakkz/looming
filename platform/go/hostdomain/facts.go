// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Machine-capacity sanity bounds for the join-carried facts
// (advisor-l1 §8 slice 1.3). The lower bound is 0 — a negative fact is
// a collector bug, never a value — and the upper bounds fence the
// unit-confusion class of bugs (bytes reported as megabytes, blocks as
// gigabytes) without pretending to know tomorrow's hardware.
const (
	maxCPUCores = 1024
	maxMemoryMB = 1 << 24 // 16 TiB
	maxDiskGB   = 1 << 22 // 4 PiB
	maxLatency  = 1 << 20 // ~17 minutes one-way is not a LAN figure
)

// collectedAtHorizon rejects collected_at stamps further than a day
// into the future — clock skew on the joining host must not poison the
// staleness picture on the server.
const collectedAtHorizon = 24 * time.Hour

// Arch vocabulary the facts accept. config.Capabilities pins the same
// two values for the declared file side (advisor-l1 §2); the wire
// facts and the file facts must draw from one vocabulary, so the
// domain owns the acceptance check and the config layer re-validates
// on load.
const (
	archX8664 = "x86_64"
	archARM64 = "arm64"
)

// ErrInvalidCapabilities marks a join-carried capabilities block that
// fails the strict wire contract — unknown keys, out-of-range values,
// or a non-vocabulary arch. The join answers 400 invalid_request with
// this detail: the joining operator can see and fix the payload.
var ErrInvalidCapabilities = errors.New("topology: invalid host capabilities")

// Capabilities is one host's observed machine facts — the advisor's
// evaluator input discovered at join time (advisor-l1 §8 slice 1.3)
// where slice 1.1 hand-declared it in the topology file. nil on the
// Host means no facts were collected (an old CLI's join): a legal
// state, stored as SQL NULL. CollectedAt is the joining host's own
// timestamp; it is always set when facts arrive.
type Capabilities struct {
	Hardware    HardwareCapabilities `json:"hardware"`
	Network     NetworkCapabilities  `json:"network"`
	CollectedAt time.Time            `json:"collected_at"`
}

// HardwareCapabilities is the observed compute shape. Zero-valued
// scalars mean "not observed" — the collector degrades per-fact, and
// the evaluator reads the zeros as the missing-fact gap (the same
// fail-closed contract the declared side has).
type HardwareCapabilities struct {
	CPUCores int    `json:"cpu_cores"`
	MemoryMB int    `json:"memory_mb"`
	DiskGB   int    `json:"disk_gb"`
	Arch     string `json:"arch"`
}

// NetworkCapabilities is the observed network shape: Egress is a
// pointer because observed-false and not-observed must stay distinct
// (the declared side's three-state contract), and LatenciesMS carries
// observed one-way figures keyed by target host id when a future
// collector measures them — slice 1.3's join collector sends none.
type NetworkCapabilities struct {
	Egress      *bool          `json:"egress"`
	LatenciesMS map[string]int `json:"latencies_ms,omitempty"`
}

// ParseCapabilities validates and decodes one join-carried
// capabilities block. The decode is strict — an unknown key is a
// protocol error, not a silent default (the same philosophy as the
// topology file's strict YAML decode) — and the value checks enforce
// the sanity bounds, the arch vocabulary, and a collected_at that
// parses as RFC 3339 and is not more than a day ahead of the server's
// now (clock-skew fence). An empty block is legal: it means the
// collector ran and observed nothing, which the evaluator reads as
// the all-missing gap.
func ParseCapabilities(data []byte, now time.Time) (*Capabilities, error) {
	var caps Capabilities
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&caps); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCapabilities, err)
	}
	// One document, exactly: trailing values mean a concatenated or
	// truncated payload, not a facts block.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data after the capabilities object", ErrInvalidCapabilities)
	}
	if err := caps.Validate(now); err != nil {
		return nil, err
	}
	return &caps, nil
}

// Validate enforces the fact-value contract on an in-memory block.
// now fences collected_at; pass time.Now() at the boundary that owns
// the clock (the join handler).
func (c *Capabilities) Validate(now time.Time) error {
	hw, nw := c.Hardware, c.Network
	switch {
	case hw.CPUCores < 0 || hw.CPUCores > maxCPUCores:
		return fmt.Errorf("%w: hardware.cpu_cores is %d, must be between 0 and %d", ErrInvalidCapabilities, hw.CPUCores, maxCPUCores)
	case hw.MemoryMB < 0 || hw.MemoryMB > maxMemoryMB:
		return fmt.Errorf("%w: hardware.memory_mb is %d, must be between 0 and %d", ErrInvalidCapabilities, hw.MemoryMB, maxMemoryMB)
	case hw.DiskGB < 0 || hw.DiskGB > maxDiskGB:
		return fmt.Errorf("%w: hardware.disk_gb is %d, must be between 0 and %d", ErrInvalidCapabilities, hw.DiskGB, maxDiskGB)
	case hw.Arch != "" && hw.Arch != archX8664 && hw.Arch != archARM64:
		return fmt.Errorf("%w: hardware.arch %q must be %q or %q", ErrInvalidCapabilities, hw.Arch, archX8664, archARM64)
	}
	for target, ms := range nw.LatenciesMS {
		if ms < 0 || ms > maxLatency {
			return fmt.Errorf("%w: network.latencies_ms[%q] is %d, must be between 0 and %d", ErrInvalidCapabilities, target, ms, maxLatency)
		}
	}
	if c.CollectedAt.IsZero() {
		return fmt.Errorf("%w: collected_at is required", ErrInvalidCapabilities)
	}
	if c.CollectedAt.After(now.Add(collectedAtHorizon)) {
		return fmt.Errorf("%w: collected_at %s is more than %s ahead of the server clock",
			ErrInvalidCapabilities, c.CollectedAt.UTC().Format(time.RFC3339), collectedAtHorizon)
	}
	return nil
}
