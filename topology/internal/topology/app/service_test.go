// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kikakkz/looming/topology/internal/topology/app"
	"github.com/kikakkz/looming/topology/internal/topology/domain"
)

// fakeStore is the topology Store double: scripted current state, call
// counts for the idempotency contract, and scripted errors.
type fakeStore struct {
	current    domain.Topology
	currentErr error
	saveErr    error

	saves []domain.Topology
}

func (f *fakeStore) Save(_ context.Context, t domain.Topology) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saves = append(f.saves, t)
	f.current = t
	f.currentErr = nil
	return nil
}

func (f *fakeStore) Current(_ context.Context) (domain.Topology, error) {
	if f.currentErr != nil {
		return domain.Topology{}, f.currentErr
	}
	return f.current, nil
}

var (
	ctx  = context.Background()
	now  = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tick = func() time.Time { return now }
)

const (
	hostA = "11111111-1111-1111-1111-111111111111"
	hostB = "22222222-2222-2222-2222-222222222222"
	hostC = "33333333-3333-3333-3333-333333333333"
)

func validPlacements() []domain.ComponentPlacement {
	return []domain.ComponentPlacement{
		{Component: domain.ComponentGatewayFront, HostID: hostA, Ports: map[string]int{"http": 8080}},
		{Component: "engine", HostID: hostB, Ports: map[string]int{"grpc": 9090}},
	}
}

func publicAccess(t *testing.T) domain.Access {
	t.Helper()
	a, err := domain.NewAccess("public", "direct", "http")
	assert.NoError(t, err)
	return a
}

func newService(store *fakeStore) *app.Service {
	return app.NewService(store, tick)
}

func TestDeclarePersistsFirstTopologyAtRevisionOne(t *testing.T) {
	store := &fakeStore{currentErr: domain.ErrNoTopology}
	svc := newService(store)

	got, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements(),
		Access:     publicAccess(t),
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(1), got.Revision)
	assert.Equal(t, domain.SingletonID, got.ID)
	assert.Equal(t, now, got.UpdatedAt)
	assert.Len(t, store.saves, 1)
	assert.Equal(t, int64(1), store.saves[0].Revision)
}

func TestDeclareIsConvergeIdempotent(t *testing.T) {
	store := &fakeStore{currentErr: domain.ErrNoTopology}
	svc := newService(store)

	first, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements(),
		Access:     publicAccess(t),
	})
	assert.NoError(t, err)

	// Identical declare: no save, no revision bump — same state back.
	second, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements(),
		Access:     publicAccess(t),
	})
	assert.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Len(t, store.saves, 1, "an identical declare must not write")
}

func TestDeclareTreatsReorderedPlacementsAsNoOp(t *testing.T) {
	store := &fakeStore{currentErr: domain.ErrNoTopology}
	svc := newService(store)

	_, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements(),
		Access:     publicAccess(t),
	})
	assert.NoError(t, err)

	reordered := []domain.ComponentPlacement{validPlacements()[1], validPlacements()[0]}
	got, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: reordered,
		Access:     publicAccess(t),
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(1), got.Revision)
	assert.Len(t, store.saves, 1)
}

func TestDeclareBumpsRevisionOnChange(t *testing.T) {
	store := &fakeStore{currentErr: domain.ErrNoTopology}
	svc := newService(store)

	_, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements(),
		Access:     publicAccess(t),
	})
	assert.NoError(t, err)

	private, err := domain.NewAccess("private", "direct", "ip")
	assert.NoError(t, err)
	got, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements(),
		Access:     private,
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(2), got.Revision)
	assert.Len(t, store.saves, 2)
}

func TestDeclareRejectsEmptyHosts(t *testing.T) {
	svc := newService(&fakeStore{})
	_, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      nil,
		Placements: validPlacements(),
		Access:     publicAccess(t),
	})
	assert.ErrorIs(t, err, domain.ErrNoHosts)
}

func TestDeclareRejectsPlacementOnUndeclaredHost(t *testing.T) {
	svc := newService(&fakeStore{})
	placements := validPlacements()
	placements[0].HostID = hostC
	_, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: placements,
		Access:     publicAccess(t),
	})
	assert.ErrorIs(t, err, domain.ErrUndeclaredHost)
}

func TestDeclareRejectsDuplicateComponentHostPair(t *testing.T) {
	svc := newService(&fakeStore{})
	placements := append(validPlacements(), domain.ComponentPlacement{
		Component: "engine", HostID: hostB, Ports: map[string]int{"grpc": 9091},
	})
	_, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: placements,
		Access:     publicAccess(t),
	})
	assert.ErrorIs(t, err, domain.ErrDuplicatePlacement)
}

func TestDeclareRejectsWrongGatewayFrontCount(t *testing.T) {
	svc := newService(&fakeStore{})

	// Zero.
	_, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements()[1:],
		Access:     publicAccess(t),
	})
	assert.ErrorIs(t, err, domain.ErrGatewayFrontCount)

	// Two.
	placements := append(validPlacements(), domain.ComponentPlacement{
		Component: domain.ComponentGatewayFront, HostID: hostB, Ports: map[string]int{"http": 8081},
	})
	_, err = svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: placements,
		Access:     publicAccess(t),
	})
	assert.ErrorIs(t, err, domain.ErrGatewayFrontCount)
}

func TestDeclareRejectsPortConflicts(t *testing.T) {
	svc := newService(&fakeStore{})
	placements := validPlacements()
	placements[1].HostID = hostA // engine joins the gateway front's host
	placements[1].Ports = map[string]int{"grpc": 8080}
	_, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: placements,
		Access:     publicAccess(t),
	})
	assert.ErrorIs(t, err, domain.ErrPortConflict)
}

func TestDeclareRejectsPhase2AccessWithIssueNamed(t *testing.T) {
	svc := newService(&fakeStore{})
	_, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements(),
		Access:     domain.Access{Mode: "public", Transport: "vip", Endpoint: "ip"},
	})
	assert.ErrorIs(t, err, domain.ErrTransportNotPhase1)
	assert.Contains(t, err.Error(), "#109")
}

func TestDeclarePropagatesStoreErrors(t *testing.T) {
	boom := errors.New("db unavailable")

	store := &fakeStore{currentErr: boom}
	svc := newService(store)
	_, err := svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements(),
		Access:     publicAccess(t),
	})
	assert.ErrorIs(t, err, boom)

	store = &fakeStore{currentErr: domain.ErrNoTopology, saveErr: domain.ErrConflict}
	svc = newService(store)
	_, err = svc.Declare(ctx, app.DeclareInput{
		Hosts:      []string{hostA, hostB},
		Placements: validPlacements(),
		Access:     publicAccess(t),
	})
	assert.ErrorIs(t, err, domain.ErrConflict)
}
