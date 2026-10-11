// SPDX-License-Identifier: Apache-2.0

// Package factscmd is the admin face's topology-facts command group:
// `looming topology facts pull` merges the machine facts topologyd
// observed at join time (advisor-l1 §8 slice 1.3) into the topology
// file's declared capabilities. The file stays the source of truth —
// the operator reviews the diff like any other edit — and the merge
// protects what observation cannot know: the cloud/lan zone and the
// host labels stay operator-declared, observed values only ever
// replace hardware figures, egress, and latencies.
package factscmd

import (
	"fmt"
	"maps"
	"slices"

	"github.com/kikakkz/looming/platform/go/config"
	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
	joinadapter "github.com/kikakkz/looming/platform/go/joinadapter"
)

// match pairs one file host with the observed host that represents
// it. Merging happens at the pair level in runPull, next to the
// reporting that consumes the change list.
type match struct {
	declared config.Host
	observed joinadapter.ObservedHost
}

// fieldChange is one operator-visible difference the pull will write.
type fieldChange struct {
	field string
	from  string // "unset" when the declared side had nothing
	to    string
}

// matchHosts pairs observed hosts with file hosts: id first (identity
// wins), address second for hosts whose server-side id was minted
// (join without --host-id) while the file carries the operator's id.
// Every observed host is consumed at most once; leftovers are reported,
// never guessed.
func matchHosts(declared []config.Host, observed []joinadapter.ObservedHost) (matched []match, unmatched []joinadapter.ObservedHost) {
	byID := make(map[string]config.Host, len(declared))
	for _, h := range declared {
		byID[h.ID] = h
	}

	consumed := make(map[string]bool, len(observed))
	used := make(map[string]bool, len(declared))
	for _, obs := range observed {
		if h, ok := byID[obs.ID]; ok && !used[h.ID] {
			matched = append(matched, match{declared: h, observed: obs})
			used[h.ID] = true
			consumed[obs.ID] = true
		}
	}
	for _, obs := range observed {
		if consumed[obs.ID] {
			continue
		}
		for _, h := range declared {
			if used[h.ID] || h.Address != obs.Address {
				continue
			}
			matched = append(matched, match{declared: h, observed: obs})
			used[h.ID] = true
			consumed[obs.ID] = true
			break
		}
	}
	for _, obs := range observed {
		if !consumed[obs.ID] {
			unmatched = append(unmatched, obs)
		}
	}
	return matched, unmatched
}

// mergeFacts folds the observed block into the declared capabilities
// and reports the per-field differences. Observed values win only
// where observation is authoritative — hardware scalars the collector
// read as non-zero, egress the probe measured, latencies a collector
// recorded. The zone stays declared: cloud/lan is a placement semantic
// (advisor-l1 §2), not something a probe can know. A nil declared
// block with no observed facts stays nil: pull never invents a block.
func mergeFacts(declared *config.Capabilities, observed *hostdomain.Capabilities) (*config.Capabilities, []fieldChange) {
	if observed == nil {
		return declared, nil
	}
	var merged config.Capabilities
	if declared != nil {
		merged = *declared
		merged.Network.LatenciesMS = maps.Clone(declared.Network.LatenciesMS)
	}

	changes := mergeHardwareFacts(&merged, observed.Hardware)
	changes = append(changes, mergeNetworkFacts(&merged, observed.Network)...)

	if len(changes) == 0 && declared == nil {
		return nil, nil
	}
	return &merged, changes
}

// mergeHardwareFacts applies the observed compute figures, one slot at
// a time: a non-zero observation that differs from the declared value
// replaces it and is recorded.
func mergeHardwareFacts(merged *config.Capabilities, hw hostdomain.HardwareCapabilities) []fieldChange {
	var changes []fieldChange
	changes = mergeIntFact(changes, "cpu_cores", &merged.Hardware.CPUCores, hw.CPUCores)
	changes = mergeIntFact(changes, "memory_mb", &merged.Hardware.MemoryMB, hw.MemoryMB)
	changes = mergeIntFact(changes, "disk_gb", &merged.Hardware.DiskGB, hw.DiskGB)
	if hw.Arch != "" && hw.Arch != merged.Hardware.Arch {
		changes = append(changes, fieldChange{field: "arch", from: declaredValue(merged.Hardware.Arch), to: hw.Arch})
		merged.Hardware.Arch = hw.Arch
	}
	return changes
}

// mergeIntFact replaces one int slot when the observation is non-zero
// and differs, recording the change.
func mergeIntFact(changes []fieldChange, field string, slot *int, observed int) []fieldChange {
	if observed == 0 || observed == *slot {
		return changes
	}
	changes = append(changes, fieldChange{field: field, from: declaredValue(*slot), to: fmt.Sprintf("%d", observed)})
	*slot = observed
	return changes
}

// mergeNetworkFacts applies the observed network figures: egress
// replaces whenever the probe produced a value (declared and observed
// false agree: no change line, value kept), latencies replace as a
// set. The zone is deliberately not a parameter here.
func mergeNetworkFacts(merged *config.Capabilities, nw hostdomain.NetworkCapabilities) []fieldChange {
	var changes []fieldChange
	if nw.Egress != nil {
		var declaredEgress string
		switch {
		case merged.Network.Egress == nil:
			declaredEgress = "unset"
		case *merged.Network.Egress:
			declaredEgress = "true"
		default:
			declaredEgress = "false"
		}
		observedEgress := fmt.Sprintf("%t", *nw.Egress)
		if declaredEgress != observedEgress {
			changes = append(changes, fieldChange{field: "egress", from: declaredEgress, to: observedEgress})
		}
		merged.Network.Egress = nw.Egress
	}
	if nw.LatenciesMS != nil && !maps.Equal(nw.LatenciesMS, merged.Network.LatenciesMS) {
		changes = append(changes, fieldChange{
			field: "latencies_ms",
			from:  declaredValue(renderLatencies(merged.Network.LatenciesMS)),
			to:    renderLatencies(nw.LatenciesMS),
		})
		merged.Network.LatenciesMS = maps.Clone(nw.LatenciesMS)
	}
	return changes
}

// declaredValue renders a declared-side value for the change summary,
// "unset" standing in for the absent fact.
func declaredValue(v any) string {
	switch value := v.(type) {
	case int:
		if value == 0 {
			return "unset"
		}
		return fmt.Sprintf("%d", value)
	case string:
		if value == "" {
			return "unset"
		}
		return value
	default:
		return fmt.Sprintf("%v", value)
	}
}

// renderLatencies prints the latency map in deterministic key order
// for the change summary.
func renderLatencies(lat map[string]int) string {
	if lat == nil {
		return "unset"
	}
	keys := slices.Sorted(maps.Keys(lat))
	out := "{"
	for i, k := range keys {
		if i > 0 {
			out += " "
		}
		out += fmt.Sprintf("%s:%d", k, lat[k])
	}
	return out + "}"
}
