// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostadapter "github.com/kikakkz/looming/platform/go/hostadapter"
	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
	adapter "github.com/kikakkz/looming/platform/go/joinadapter"
	domain "github.com/kikakkz/looming/platform/go/joindomain"
	"github.com/kikakkz/looming/platform/go/tests/pgtest"
)

var ctx = context.Background()

func testToken(t *testing.T, role string, ttl time.Duration, now time.Time) *domain.JoinToken {
	t.Helper()
	sum := sha256.Sum256([]byte("raw-" + role + now.String()))
	return &domain.JoinToken{
		TokenHash: sum[:],
		Role:      role,
		CreatedBy: "integration",
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
}

func TestTokenStoreRoundTrip(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewTokenStore(db)
	now := time.Now().UTC().Truncate(time.Microsecond)

	tok := testToken(t, domain.RoleEngine, time.Hour, now)
	require.NoError(t, store.Create(ctx, tok))

	got, err := store.ByHash(ctx, tok.TokenHash)
	require.NoError(t, err)
	assert.Equal(t, tok.Role, got.Role)
	assert.Equal(t, tok.CreatedBy, got.CreatedBy)
	assert.True(t, tok.ExpiresAt.Equal(got.ExpiresAt), "expiry instant mismatch: %v vs %v", tok.ExpiresAt, got.ExpiresAt)
	assert.False(t, got.CreatedAt.IsZero(), "0003 backfills created_at")
	assert.Nil(t, got.UsedAt)

	_, err = store.ByHash(ctx, domain.HashToken("never-minted"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenNotFound))
}

func TestTokenStoreMarkUsedIsExactlyOnce(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewTokenStore(db)
	now := time.Now().UTC()

	tok := testToken(t, domain.RoleWorker, time.Hour, now)
	require.NoError(t, store.Create(ctx, tok))

	usedAt := now.Add(time.Minute).Truncate(time.Microsecond)
	require.NoError(t, store.MarkUsed(ctx, tok.TokenHash, usedAt))

	got, err := store.ByHash(ctx, tok.TokenHash)
	require.NoError(t, err)
	require.NotNil(t, got.UsedAt)
	assert.True(t, usedAt.Equal(*got.UsedAt), "used-at instant mismatch: %v vs %v", usedAt, *got.UsedAt)

	err = store.MarkUsed(ctx, tok.TokenHash, usedAt.Add(time.Minute))
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenUsed), "a repeated consume is a conflict, never a double-write")

	// An unknown hash fails with the same conflict the raced path uses.
	err = store.MarkUsed(ctx, domain.HashToken("unknown"), usedAt)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenUsed))
}

func TestTokenStoreConcurrentConsumeSingleWinner(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewTokenStore(db)
	now := time.Now().UTC()

	tok := testToken(t, domain.RoleEngine, time.Hour, now)
	require.NoError(t, store.Create(ctx, tok))

	// Both racers pass a used/expiry pre-check; the guarded UPDATE must
	// let exactly one win — the slice-A invite-token precedent.
	const racers = 8
	var wg sync.WaitGroup
	results := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- store.MarkUsed(ctx, tok.TokenHash, now.Add(time.Minute))
		}()
	}
	wg.Wait()
	close(results)

	var wins int
	for err := range results {
		if err == nil {
			wins++
			continue
		}
		assert.True(t, errors.Is(err, domain.ErrTokenUsed))
	}
	assert.Equal(t, 1, wins, "exactly one concurrent consume may succeed")
}

func TestTokenStoreListOrdersNewestFirst(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewTokenStore(db)
	base := time.Now().UTC()

	for i, role := range []string{domain.RoleEngine, domain.RoleWorker, domain.RoleEngine} {
		require.NoError(t, store.Create(ctx, testToken(t, role, time.Hour, base.Add(time.Duration(i)*time.Minute))))
	}

	list, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3)
	for i := 1; i < len(list); i++ {
		assert.False(t, list[i-1].CreatedAt.Before(list[i].CreatedAt), "newest first")
	}
}

func TestHostRegistryAcceptsJoinedHostIDs(t *testing.T) {
	// The T2 contract: joined hosts carry host-<uuid8> ids — migration
	// 0003 widened hosts.id to text so this round-trips.
	db := pgtest.NewDB(t)
	registry := hostadapter.NewRegistry(db)
	now := time.Now().UTC()

	raw, hash, err := hostdomain.GenerateCredential(newTestRNG())
	require.NoError(t, err)
	host, err := hostdomain.NewHost("host-0badc0de", "10.0.0.42", []string{"role=engine"}, now)
	require.NoError(t, err)
	host.CredentialHash = hash

	stored, err := registry.Register(ctx, host)
	require.NoError(t, err)
	assert.Equal(t, "host-0badc0de", stored.ID)

	byID, err := registry.ByID(ctx, "host-0badc0de")
	require.NoError(t, err)
	assert.True(t, byID.CredentialMatches(raw), "the minted credential verifies against the stored hash")

	// The deterministic uuid5 ids apply uses keep working too.
	applyHost, err := hostdomain.NewHost("11111111-1111-1111-1111-111111111111", "10.0.0.43", nil, now)
	require.NoError(t, err)
	_, err = registry.Register(ctx, applyHost)
	require.NoError(t, err)
}

// newTestRNG returns a deterministic 64-byte stream — enough for one
// credential mint.
func newTestRNG() *byteReader { return &byteReader{buf: make([]byte, 64)} }

type byteReader struct {
	buf []byte
	off int
}

func (b *byteReader) Read(p []byte) (int, error) {
	if b.off+len(p) > len(b.buf) {
		return 0, errors.New("test rng exhausted")
	}
	n := copy(p, b.buf[b.off:])
	b.off += n
	return n, nil
}
