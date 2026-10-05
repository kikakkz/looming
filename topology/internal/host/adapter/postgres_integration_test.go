// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/host/adapter"
	"github.com/kikakkz/looming/topology/internal/host/domain"
	"github.com/kikakkz/looming/topology/tests/pgtest"
)

var ctx = context.Background()

func newHost(t *testing.T, id, address string, labels ...string) *domain.Host {
	t.Helper()
	h, err := domain.NewHost(id, address, labels, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	return h
}

// assertSameHost compares hosts field-wise: postgres returns timestamps
// with the session location, so time instant equality is checked
// explicitly instead of struct equality.
func assertSameHost(t *testing.T, want, got *domain.Host) {
	t.Helper()
	assert.Equal(t, want.ID, got.ID)
	assert.Equal(t, want.Address, got.Address)
	assert.Equal(t, want.RoleLabels, got.RoleLabels)
	assert.True(t, bytes.Equal(want.CredentialHash, got.CredentialHash),
		"credential hash mismatch: %x vs %x", want.CredentialHash, got.CredentialHash)
	assert.True(t, want.JoinedAt.Equal(got.JoinedAt),
		"joined-at instant mismatch: %v vs %v", want.JoinedAt, got.JoinedAt)
}

func TestRegistryRegisterAndLookupRoundTrip(t *testing.T) {
	reg := adapter.NewRegistry(pgtest.NewDB(t))

	h, err := reg.Register(ctx, newHost(t, "11111111-1111-1111-1111-111111111111", "10.0.0.1", "engine"))
	require.NoError(t, err)
	assert.Nil(t, h.CredentialHash, "the re-join credential slot starts empty")

	byID, err := reg.ByID(ctx, h.ID)
	require.NoError(t, err)
	assertSameHost(t, h, byID)

	byAddress, err := reg.ByAddress(ctx, "10.0.0.1")
	require.NoError(t, err)
	assertSameHost(t, h, byAddress)

	_, err = reg.ByID(ctx, "99999999-9999-9999-9999-999999999999")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = reg.ByAddress(ctx, "10.9.9.9")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestRegistryRegisterRejectsTakenAddress(t *testing.T) {
	reg := adapter.NewRegistry(pgtest.NewDB(t))

	_, err := reg.Register(ctx, newHost(t, "11111111-1111-1111-1111-111111111111", "10.0.0.1", "engine"))
	require.NoError(t, err)

	_, err = reg.Register(ctx, newHost(t, "22222222-2222-2222-2222-222222222222", "10.0.0.1", "engine"))
	assert.ErrorIs(t, err, domain.ErrAddressTaken)
}

func TestRegistryRegisterSameIDIsIdempotentRefresh(t *testing.T) {
	reg := adapter.NewRegistry(pgtest.NewDB(t))

	first, err := reg.Register(ctx, newHost(t, "11111111-1111-1111-1111-111111111111", "10.0.0.1", "engine"))
	require.NoError(t, err)

	again, err := reg.Register(ctx, newHost(t, "11111111-1111-1111-1111-111111111111", "10.0.0.1", "engine", "gpu"))
	require.NoError(t, err)
	assert.Equal(t, []string{"engine", "gpu"}, again.RoleLabels)
	assert.True(t, first.JoinedAt.Equal(again.JoinedAt), "re-registration keeps the original join time")

	_, err = reg.ByAddress(ctx, "10.0.0.1")
	require.NoError(t, err)
}

func TestRegistryUpdateMovesAddressAndLabels(t *testing.T) {
	reg := adapter.NewRegistry(pgtest.NewDB(t))

	h, err := reg.Register(ctx, newHost(t, "11111111-1111-1111-1111-111111111111", "10.0.0.1", "engine"))
	require.NoError(t, err)
	other, err := reg.Register(ctx, newHost(t, "22222222-2222-2222-2222-222222222222", "10.0.0.2", "engine"))
	require.NoError(t, err)

	moved := *h
	moved.Address = "10.0.0.9"
	moved.RoleLabels = []string{"engine", "gpu"}
	updated, err := reg.Update(ctx, &moved)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.9", updated.Address)
	assert.Equal(t, []string{"engine", "gpu"}, updated.RoleLabels)
	assert.True(t, h.JoinedAt.Equal(updated.JoinedAt), "Update never rewrites the join time")

	_, err = reg.ByAddress(ctx, "10.0.0.1")
	assert.ErrorIs(t, err, domain.ErrNotFound, "the old address is released by the move")

	_, err = reg.Update(ctx, newHost(t, "99999999-9999-9999-9999-999999999999", "10.9.9.9"))
	assert.ErrorIs(t, err, domain.ErrNotFound)

	blocked := *h
	blocked.Address = other.Address
	_, err = reg.Update(ctx, &blocked)
	assert.ErrorIs(t, err, domain.ErrAddressTaken)
}
