// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	domain "github.com/kikakkz/looming/platform/go/topologydomain"
)

func TestValidatePlacementsAcceptsPhase1Set(t *testing.T) {
	assert.NoError(t, domain.ValidatePlacements([]string{hostA, hostB}, somePlacements()))
}

func TestValidatePlacementsRejectsUndeclaredHost(t *testing.T) {
	placements := somePlacements()
	placements[0].HostID = hostC
	err := domain.ValidatePlacements([]string{hostA, hostB}, placements)
	assert.ErrorIs(t, err, domain.ErrUndeclaredHost)
}

func TestValidatePlacementsRejectsDuplicateComponentHostPair(t *testing.T) {
	placements := somePlacements()
	placements = append(placements, domain.ComponentPlacement{
		Component: "engine", HostID: hostA, Ports: map[string]int{"grpc": 9093},
	})
	err := domain.ValidatePlacements([]string{hostA, hostB}, placements)
	assert.ErrorIs(t, err, domain.ErrDuplicatePlacement)
}

func TestValidatePlacementsRequiresExactlyOneGatewayFront(t *testing.T) {
	// Zero gateway-front placements.
	zero := []domain.ComponentPlacement{
		{Component: "engine", HostID: hostA, Ports: map[string]int{"grpc": 9090}},
	}
	err := domain.ValidatePlacements([]string{hostA}, zero)
	assert.ErrorIs(t, err, domain.ErrGatewayFrontCount)

	// Two gateway-front placements (#109 lifts the invariant to N).
	two := []domain.ComponentPlacement{
		{Component: domain.ComponentGatewayFront, HostID: hostA, Ports: map[string]int{"http": 8080}},
		{Component: domain.ComponentGatewayFront, HostID: hostB, Ports: map[string]int{"http": 8080}},
	}
	err = domain.ValidatePlacements([]string{hostA, hostB}, two)
	assert.ErrorIs(t, err, domain.ErrGatewayFrontCount)
}

func TestValidatePlacementsRejectsPortConflictsOnOneHost(t *testing.T) {
	placements := []domain.ComponentPlacement{
		{Component: domain.ComponentGatewayFront, HostID: hostA, Ports: map[string]int{"http": 8080}},
		{Component: "engine", HostID: hostA, Ports: map[string]int{"grpc": 8080}},
	}
	err := domain.ValidatePlacements([]string{hostA}, placements)
	assert.ErrorIs(t, err, domain.ErrPortConflict)
}

func TestValidatePlacementsAllowsSamePortOnDifferentHosts(t *testing.T) {
	placements := []domain.ComponentPlacement{
		{Component: domain.ComponentGatewayFront, HostID: hostA, Ports: map[string]int{"http": 8080}},
		{Component: domain.ComponentGatewayFront, HostID: hostB, Ports: map[string]int{"http": 8080}},
	}
	// Blocked by the gateway-front invariant, not the port rule — so use
	// the port rule's own pairing: same number, different hosts, one
	// gateway front.
	placements[1].Component = "engine"
	placements[1].HostID = hostB
	assert.NoError(t, domain.ValidatePlacements([]string{hostA, hostB}, placements))
}

// TestMatchesIsSensitiveToExtraHosts pins the converge-idempotency
// contract for the compose host-alias channel: an extra_hosts change
// is a placement change — the declare must not no-op on it, or the new
// alias would never converge.
func TestMatchesIsSensitiveToExtraHosts(t *testing.T) {
	access := domain.Access{}
	topo := domain.Uninitialized().Next(access, []domain.ComponentPlacement{{
		Component:  domain.ComponentGatewayFront,
		HostID:     hostA,
		Ports:      map[string]int{"http": 8080},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	}}, time.Now())
	assert.True(t, topo.Matches(access, []domain.ComponentPlacement{{
		Component:  domain.ComponentGatewayFront,
		HostID:     hostA,
		Ports:      map[string]int{"http": 8080},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	}}))
	assert.False(t, topo.Matches(access, []domain.ComponentPlacement{{
		Component: domain.ComponentGatewayFront,
		HostID:    hostA,
		Ports:     map[string]int{"http": 8080},
	}}))
}

// TestNextDeepCopiesExtraHosts pins the aggregate aliasing rule for
// the new placement field: mutating the caller's slice after Next must
// not move the persisted snapshot.
func TestNextDeepCopiesExtraHosts(t *testing.T) {
	hosts := []string{"host.docker.internal:host-gateway"}
	topo := domain.Uninitialized().Next(domain.Access{}, []domain.ComponentPlacement{{
		Component:  domain.ComponentGatewayFront,
		HostID:     hostA,
		Ports:      map[string]int{"http": 8080},
		ExtraHosts: hosts,
	}}, time.Now())
	hosts[0] = "tampered:10.0.0.99"
	assert.Equal(t, "host.docker.internal:host-gateway", topo.Placements[0].ExtraHosts[0])
}

// TestMatchesIgnoresExtraHostsOrder pins the converge-idempotency
// refinement: the renderer sorts extra_hosts, so a declaration-order
// change produces the identical compose artifact and must not register
// as a placement change.
func TestMatchesIgnoresExtraHostsOrder(t *testing.T) {
	access := domain.Access{}
	topo := domain.Uninitialized().Next(access, []domain.ComponentPlacement{{
		Component:  domain.ComponentGatewayFront,
		HostID:     hostA,
		Ports:      map[string]int{"http": 8080},
		ExtraHosts: []string{"b.example:10.0.0.2", "a.example:10.0.0.1"},
	}}, time.Now())
	assert.True(t, topo.Matches(access, []domain.ComponentPlacement{{
		Component:  domain.ComponentGatewayFront,
		HostID:     hostA,
		Ports:      map[string]int{"http": 8080},
		ExtraHosts: []string{"a.example:10.0.0.1", "b.example:10.0.0.2"},
	}}))
}
