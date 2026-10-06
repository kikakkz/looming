// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// Placement-set invariants (topology-l1 §5: ComponentPlacement and
// Topology rows). These are aggregate rules over the whole declare
// input, so they are functions of the set, not of one placement.
var (
	ErrUndeclaredHost     = errors.New("topology: placement references an undeclared host")
	ErrDuplicatePlacement = errors.New("topology: component placed twice on one host")
	ErrGatewayFrontCount  = errors.New("topology: exactly one gateway-front placement required")
	ErrPortConflict       = errors.New("topology: two placements claim the same port on one host")
)

// ComponentGatewayFront is the single gateway-front placement's
// component name; the phase-1 invariant requires exactly one (#109
// lifts the invariant to N).
const ComponentGatewayFront = "gateway-front"

// ComponentIdentityd is the identity service's placement name — the
// second member of the phase-1 render allowlist (T1).
const ComponentIdentityd = "identityd"

// ComponentTopologyd is the topology service's placement name — the
// join/rejoin API host (topology-l1 §7), added to the phase-1 allowlist
// by T2. The YAML declares its placement like any other component
// (typically the state host, but never forced there).
const ComponentTopologyd = "topologyd"

// ComponentPlacement is one component's desired placement: which host
// it runs on, the named ports it claims, free-form config, and optional
// compose-level host aliases (extra_hosts). The (Component, HostID)
// pair is unique within a declare set — the invariant is enforced here
// and by the placements table's UNIQUE(component, host_id) constraint.
type ComponentPlacement struct {
	Component string
	HostID    string
	Ports     map[string]int
	Config    map[string]string
	// ExtraHosts is the compose service's extra_hosts passthrough:
	// "hostname:address" entries (the docker host-gateway alias shape
	// included) a container needs that plain env wiring cannot
	// express. It renders into the service map only — never into the
	// inline environment.
	ExtraHosts []string
}

// ValidatePlacements enforces the phase-1 placement-set invariants
// against the declared host IDs: every placement references a declared
// host, (component, host) pairs are unique, exactly one gateway-front
// placement exists, and no host carries two placements claiming the
// same port number. hosts must be the non-empty declared set (the app
// rejects an empty set with ErrNoHosts before calling).
func ValidatePlacements(hosts []string, placements []ComponentPlacement) error {
	declared := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		declared[h] = true
	}
	seen := make(map[string]bool, len(placements))
	gatewayFronts := 0
	claimed := make(map[string]map[int]string) // host -> port -> component

	for _, p := range placements {
		if !declared[p.HostID] {
			return fmt.Errorf("%w: component %q on host %q", ErrUndeclaredHost, p.Component, p.HostID)
		}
		pair := p.Component + "\x00" + p.HostID
		if seen[pair] {
			return fmt.Errorf("%w: %q on host %q", ErrDuplicatePlacement, p.Component, p.HostID)
		}
		seen[pair] = true

		if p.Component == ComponentGatewayFront {
			gatewayFronts++
		}
		for _, port := range p.Ports {
			if owner, taken := claimed[p.HostID][port]; taken {
				return fmt.Errorf("%w: port %d on host %q claimed by both %q and %q",
					ErrPortConflict, port, p.HostID, owner, p.Component)
			}
			if claimed[p.HostID] == nil {
				claimed[p.HostID] = make(map[int]string)
			}
			claimed[p.HostID][port] = p.Component
		}
	}

	if gatewayFronts != 1 {
		return fmt.Errorf("%w: got %d (#109 lifts the invariant to N)", ErrGatewayFrontCount, gatewayFronts)
	}
	return nil
}

// samePlacements compares two placement sets without order sensitivity.
func samePlacements(a, b []ComponentPlacement) bool {
	if len(a) != len(b) {
		return false
	}
	indexed := make(map[string]ComponentPlacement, len(a))
	for _, p := range a {
		indexed[p.Component+"\x00"+p.HostID] = p
	}
	for _, q := range b {
		p, ok := indexed[q.Component+"\x00"+q.HostID]
		if !ok || !samePlacement(p, q) {
			return false
		}
	}
	return true
}

func samePlacement(a, b ComponentPlacement) bool {
	return a.Component == b.Component &&
		a.HostID == b.HostID &&
		maps.Equal(a.Ports, b.Ports) &&
		maps.Equal(a.Config, b.Config) &&
		equalStringsUnordered(a.ExtraHosts, b.ExtraHosts)
}

// equalStringsUnordered compares two string sets ignoring order: the
// renderer sorts extra_hosts, so a declaration-order change produces
// the same compose artifact and must not bump the topology revision
// (converge-idempotency).
func equalStringsUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// copyPlacements deep-copies a placement set so aggregates never alias
// caller-owned maps.
func copyPlacements(in []ComponentPlacement) []ComponentPlacement {
	out := make([]ComponentPlacement, len(in))
	for i, p := range in {
		out[i] = ComponentPlacement{
			Component:  p.Component,
			HostID:     p.HostID,
			Ports:      maps.Clone(p.Ports),
			Config:     maps.Clone(p.Config),
			ExtraHosts: slices.Clone(p.ExtraHosts),
		}
	}
	return out
}
