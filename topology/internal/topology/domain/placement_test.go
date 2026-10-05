// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kikakkz/looming/topology/internal/topology/domain"
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
