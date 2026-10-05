// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostadapter "github.com/kikakkz/looming/topology/internal/host/adapter"
	hostdomain "github.com/kikakkz/looming/topology/internal/host/domain"
	"github.com/kikakkz/looming/topology/internal/topology/adapter"
	"github.com/kikakkz/looming/topology/internal/topology/domain"
	"github.com/kikakkz/looming/topology/tests/pgtest"
)

var ctx = context.Background()

const (
	hostA = "11111111-1111-1111-1111-111111111111"
	hostB = "22222222-2222-2222-2222-222222222222"
)

// registerHosts registers the hosts placements will reference — the
// placements table's FK demands they exist first, and T1's apply
// orchestrates register-then-declare the same way. ids[i] gets
// address 10.0.0.(i+1).
func registerHosts(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	reg := hostadapter.NewRegistry(db)
	for i, id := range ids {
		h, err := hostdomain.NewHost(id, "10.0.0."+itoa(i+1), nil, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		_, err = reg.Register(ctx, h)
		require.NoError(t, err)
	}
}

func itoa(i int) string {
	return string(rune('0' + i))
}

func access(t *testing.T, mode string) domain.Access {
	t.Helper()
	a, err := domain.NewAccess(mode, "direct", "http")
	require.NoError(t, err)
	return a
}

func gatewayAndEngine() []domain.ComponentPlacement {
	return []domain.ComponentPlacement{
		{Component: domain.ComponentGatewayFront, HostID: hostA, Ports: map[string]int{"http": 8080}, Config: map[string]string{"tls": "off"}},
		{Component: "engine", HostID: hostB, Ports: map[string]int{"grpc": 9090}},
	}
}

func TestStoreCurrentSurfacesUndecodablePlacement(t *testing.T) {
	db := pgtest.NewDB(t)
	registerHosts(t, db, hostA)
	store := adapter.NewStore(db)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	require.NoError(t, store.Save(ctx, domain.Uninitialized().Next(access(t, "public"),
		[]domain.ComponentPlacement{
			{Component: domain.ComponentGatewayFront, HostID: hostA, Ports: map[string]int{"http": 8080}},
		}, now)))

	// Corrupt the payload below the app's validation reach; a read must
	// surface the decode error, not panic or silently drop the row.
	_, err := db.ExecContext(ctx,
		`UPDATE placements SET config = '{"ports": "oops"}' WHERE component = $1`,
		domain.ComponentGatewayFront)
	require.NoError(t, err)

	_, err = store.Current(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestStoreCurrentUninitialized(t *testing.T) {
	store := adapter.NewStore(pgtest.NewDB(t))
	_, err := store.Current(ctx)
	assert.ErrorIs(t, err, domain.ErrNoTopology)
}

func TestStoreSaveCurrentRoundTrip(t *testing.T) {
	db := pgtest.NewDB(t)
	registerHosts(t, db, hostA, hostB)
	store := adapter.NewStore(db)

	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	want := domain.Uninitialized().Next(access(t, "public"), gatewayAndEngine(), now)

	require.NoError(t, store.Save(ctx, want))

	got, err := store.Current(ctx)
	require.NoError(t, err)
	assert.Equal(t, want.Revision, got.Revision)
	// The schema persists the access mode only (the topology file stays
	// the source of truth for the phase-1 transport/endpoint shape).
	assert.Equal(t, want.Access.Mode, got.Access.Mode)
	assert.True(t, want.UpdatedAt.Equal(got.UpdatedAt),
		"updated-at instant mismatch: %v vs %v", want.UpdatedAt, got.UpdatedAt)
	if diff := cmp.Diff(want.Placements, got.Placements, byComponentHost()); diff != "" {
		t.Fatalf("save/current placement round trip (-want +got):\n%s", diff)
	}
}

// byComponentHost orders placements by (component, host ID): Current
// loads them in that order, and the desired set is declared unordered.
func byComponentHost() cmp.Option {
	return cmpopts.SortSlices(func(a, b domain.ComponentPlacement) bool {
		if a.Component != b.Component {
			return a.Component < b.Component
		}
		return a.HostID < b.HostID
	})
}

func TestStoreSaveReplacesPlacementsOnNextRevision(t *testing.T) {
	db := pgtest.NewDB(t)
	registerHosts(t, db, hostA, hostB)
	store := adapter.NewStore(db)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	require.NoError(t, store.Save(ctx,
		domain.Uninitialized().Next(access(t, "public"), gatewayAndEngine(), now)))

	current, err := store.Current(ctx)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, current.Next(access(t, "private"),
		gatewayAndEngine()[:1], now.Add(time.Second))))

	got, err := store.Current(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.Revision)
	assert.Equal(t, "private", string(got.Access.Mode))
	assert.Len(t, got.Placements, 1, "the placement set is replaced wholesale, not merged")
}

func TestStoreConcurrentRevisionConflict(t *testing.T) {
	db := pgtest.NewDB(t)
	registerHosts(t, db, hostA, hostB)
	store := adapter.NewStore(db)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	placements := gatewayAndEngine()[:1]

	require.NoError(t, store.Save(ctx, domain.Uninitialized().Next(access(t, "public"), placements, now)))

	// Two writers read the same revision and race; one wins, the
	// other's optimistic guard rejects the write.
	current, err := store.Current(ctx)
	require.NoError(t, err)
	winner := current.Next(access(t, "private"), placements, now.Add(time.Second))
	loser := current.Next(access(t, "public"), placements, now.Add(2*time.Second))

	require.NoError(t, store.Save(ctx, winner))
	assert.ErrorIs(t, store.Save(ctx, loser), domain.ErrConflict)
}

func TestStoreDuplicateComponentHostPairIsRejectedAtomically(t *testing.T) {
	db := pgtest.NewDB(t)
	registerHosts(t, db, hostA)
	store := adapter.NewStore(db)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	broken := domain.Uninitialized().Next(access(t, "public"), []domain.ComponentPlacement{
		{Component: domain.ComponentGatewayFront, HostID: hostA, Ports: map[string]int{"http": 8080}},
		{Component: domain.ComponentGatewayFront, HostID: hostA, Ports: map[string]int{"http": 8081}},
	}, now)
	err := store.Save(ctx, broken)
	assert.ErrorIs(t, err, domain.ErrDuplicatePlacement)

	_, err = store.Current(ctx)
	assert.ErrorIs(t, err, domain.ErrNoTopology, "a failed save must not leave the singleton row behind")
}
