// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kikakkz/looming/identity/internal/gatewayfeed/adapter"
	feeddomain "github.com/kikakkz/looming/identity/internal/gatewayfeed/domain"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

var testTime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func seedPrincipal(t *testing.T, ctx context.Context, db *sql.DB, id, status string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO principals (id, username, kind, status, roles)
		 VALUES ($1, $2, 'human', $3, '{member}')`, id, "user-"+id[:8], status); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
}

func seedKey(t *testing.T, ctx context.Context, db *sql.DB, principalID, name, status string, createdAt time.Time) []byte {
	t.Helper()
	raw := "lk-" + uuid.NewString()
	hash := sha256.Sum256([]byte(raw))
	revokedAt := sql.NullTime{}
	if status == "revoked" {
		revokedAt = sql.NullTime{Time: createdAt.Add(time.Hour), Valid: true}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO loom_keys (id, principal_id, name, prefix, last4, key_hash, key_enc, status, created_at, revoked_at)
		 VALUES ($1, $2, $3, 'lk-abcd', 'wxyz', $4, 'sealed', $5, $6, $7)`,
		uuid.NewString(), principalID, name, hash[:], status, createdAt, revokedAt); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	return hash[:]
}

func TestStoreListKeysOnlyActivePrincipalsButKeepsRevokedStatus(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewStore(db)
	ctx := context.Background()

	active := uuid.NewString()
	disabled := uuid.NewString()
	seedPrincipal(t, ctx, db, active, "active")
	seedPrincipal(t, ctx, db, disabled, "disabled")

	activeHash := seedKey(t, ctx, db, active, "live", "active", testTime)
	revokedHash := seedKey(t, ctx, db, active, "dead", "revoked", testTime.Add(time.Minute))
	droppedHash := seedKey(t, ctx, db, disabled, "hidden", "active", testTime.Add(2*time.Minute))

	keys, err := store.ListKeys(ctx)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("want 2 keys (active principal's, revoked included), got %d", len(keys))
	}
	byHash := map[string]feeddomain.Key{}
	for _, k := range keys {
		byHash[string(k.Hash)] = k
	}
	if k, ok := byHash[string(activeHash)]; !ok || k.Status != "active" || k.PrincipalID != active {
		t.Fatalf("active key mismatch: %+v", k)
	}
	if k, ok := byHash[string(revokedHash)]; !ok || k.Status != "revoked" {
		t.Fatalf("revoked key must be listed with its status: %+v", k)
	}
	if _, ok := byHash[string(droppedHash)]; ok {
		t.Fatal("a disabled principal's key must vanish from the feed (fail closed)")
	}
}

func TestStoreListPrincipalsCarriesEveryStatus(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewStore(db)
	ctx := context.Background()

	pending := uuid.NewString()
	active := uuid.NewString()
	disabled := uuid.NewString()
	seedPrincipal(t, ctx, db, pending, "pending")
	seedPrincipal(t, ctx, db, active, "active")
	seedPrincipal(t, ctx, db, disabled, "disabled")

	principals, err := store.ListPrincipals(ctx)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	if len(principals) != 3 {
		t.Fatalf("want every principal, got %d", len(principals))
	}
	statuses := map[string]string{}
	for _, p := range principals {
		statuses[p.ID] = p.Status
	}
	if statuses[pending] != "pending" || statuses[active] != "active" || statuses[disabled] != "disabled" {
		t.Fatalf("status projection mismatch: %v", statuses)
	}
}

func TestStoreByHashJoinsPrincipalStatus(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewStore(db)
	ctx := context.Background()

	active := uuid.NewString()
	pending := uuid.NewString()
	seedPrincipal(t, ctx, db, active, "active")
	seedPrincipal(t, ctx, db, pending, "pending")

	activeHash := seedKey(t, ctx, db, active, "live", "active", testTime)
	pendingHash := seedKey(t, ctx, db, pending, "waiting", "active", testTime)
	revokedHash := seedKey(t, ctx, db, active, "dead", "revoked", testTime.Add(time.Minute))

	k, principalStatus, err := store.ByHash(ctx, activeHash)
	if err != nil {
		t.Fatalf("ByHash: %v", err)
	}
	if k.PrincipalID != active || k.Status != "active" || principalStatus != "active" {
		t.Fatalf("active join mismatch: %+v / %q", k, principalStatus)
	}

	k, principalStatus, err = store.ByHash(ctx, revokedHash)
	if err != nil {
		t.Fatalf("ByHash revoked: %v", err)
	}
	if k.Status != "revoked" || principalStatus != "active" {
		t.Fatalf("revoked key must resolve with real statuses: %+v / %q", k, principalStatus)
	}

	_, principalStatus, err = store.ByHash(ctx, pendingHash)
	if err != nil {
		t.Fatalf("ByHash pending owner: %v", err)
	}
	if principalStatus != "pending" {
		t.Fatalf("pending owner must resolve as pending: %q", principalStatus)
	}

	if _, _, err := store.ByHash(ctx, []byte("no-such-hash")); err != feeddomain.ErrNotFound {
		t.Fatalf("unknown hash: want ErrNotFound, got %v", err)
	}
}
