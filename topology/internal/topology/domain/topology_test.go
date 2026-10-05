// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"github.com/kikakkz/looming/topology/internal/topology/domain"
)

// Shared fixtures for the domain tests; wall-clock free (AD-25).

var step = time.Second

func fixedTime() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }

func someAccess(t *testing.T) domain.Access {
	t.Helper()
	a, err := domain.NewAccess("public", "direct", "http")
	assert.NoError(t, err)
	return a
}

func otherAccess(t *testing.T) domain.Access {
	t.Helper()
	a, err := domain.NewAccess("private", "direct", "ip")
	assert.NoError(t, err)
	return a
}

func somePlacements() []domain.ComponentPlacement {
	return []domain.ComponentPlacement{
		{Component: domain.ComponentGatewayFront, HostID: hostA, Ports: map[string]int{"http": 8080}, Config: map[string]string{"tls": "off"}},
		{Component: "engine", HostID: hostA, Ports: map[string]int{"grpc": 9090}},
		{Component: "engine", HostID: hostB, Ports: map[string]int{"grpc": 9091}, Config: map[string]string{"pool": "4"}},
	}
}

const (
	hostA = "11111111-1111-1111-1111-111111111111"
	hostB = "22222222-2222-2222-2222-222222222222"
	hostC = "33333333-3333-3333-3333-333333333333"
)

func TestTopologyNextBumpsRevision(t *testing.T) {
	now := fixedTime()

	initial := domain.Uninitialized()
	assert.Equal(t, int64(0), initial.Revision)
	assert.Equal(t, domain.SingletonID, initial.ID)

	declared := initial.Next(someAccess(t), somePlacements(), now)
	assert.Equal(t, int64(1), declared.Revision)
	assert.Equal(t, now, declared.UpdatedAt)

	redeclared := declared.Next(someAccess(t), somePlacements(), now.Add(step))
	assert.Equal(t, int64(2), redeclared.Revision)
}

func TestTopologyNextCopiesPlacements(t *testing.T) {
	now := fixedTime()
	placements := somePlacements()

	next := domain.Uninitialized().Next(someAccess(t), placements, now)
	placements[0].Ports["http"] = 1 // mutate the caller's input afterwards
	assert.Equal(t, 8080, next.Placements[0].Ports["http"], "Next must copy the placement maps, not alias them")
}

func TestTopologyMatchesIsOrderInsensitive(t *testing.T) {
	now := fixedTime()
	a := somePlacements()
	b := []domain.ComponentPlacement{a[1], a[2], a[0]}

	top := domain.Uninitialized().Next(someAccess(t), a, now)
	assert.True(t, top.Matches(someAccess(t), b), "same placements in a different order is the same desired state")
	assert.False(t, top.Matches(otherAccess(t), a), "different access is a different desired state")
	assert.False(t, top.Matches(someAccess(t), a[:1]), "fewer placements is a different desired state")

	// A config or port value change is a different desired state too.
	changed := somePlacements()
	changed[1].Ports["grpc"] = 9099
	assert.False(t, top.Matches(someAccess(t), changed))
}

func TestPlacementsRoundTripThroughNext(t *testing.T) {
	now := fixedTime()
	placements := somePlacements()
	top := domain.Uninitialized().Next(someAccess(t), placements, now)

	if diff := cmp.Diff(placements, top.Placements); diff != "" {
		t.Fatalf("placements must survive a Next round trip (-want +got):\n%s", diff)
	}
}
