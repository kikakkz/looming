// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kikakkz/looming/identity/internal/key/adapter"
	keydomain "github.com/kikakkz/looming/identity/internal/key/domain"
	provisionadapter "github.com/kikakkz/looming/identity/internal/provision/adapter"
	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

var mapTime = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func seedMapPrincipal(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO principals (id, username, kind, status, roles) VALUES ($1, 'map-user', 'human', 'active', '{member}')`, id); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	return id
}

// newKey builds a valid key aggregate for the given principal.
func newKey(t *testing.T, principalID, name string) *keydomain.LoomingKey {
	t.Helper()
	secret := keydomain.KeySecret("lk-" + uuid.NewString())
	k, err := keydomain.NewLoomingKey(uuid.NewString(), principalID, name, secret, []byte("sealed-"+name), mapTime)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	return k
}

func TestMapCreateWithProvisionIsAtomic(t *testing.T) {
	db := pgtest.NewDB(t)
	ctx := context.Background()
	keys := adapter.NewRepository(db)
	maps := provisionadapter.NewMapRepository(db)

	principalID := seedMapPrincipal(t, ctx, db)
	k := newKey(t, principalID, "one")
	entry, err := provisiondomain.NewIdentityMap(k.ID, "litellm", "ref-one", []byte("enc-one"), mapTime)
	if err != nil {
		t.Fatalf("map entry: %v", err)
	}
	if err := keys.CreateWithProvision(ctx, k, entry); err != nil {
		t.Fatalf("create with provision: %v", err)
	}

	byKey, err := maps.ListByKey(ctx, k.ID)
	if err != nil || len(byKey) != 1 {
		t.Fatalf("map entry must be visible after the tx: %v (%d)", err, len(byKey))
	}
	if byKey[0].CredentialRef != "ref-one" || string(byKey[0].CredentialEnc) != "enc-one" ||
		byKey[0].Status != provisiondomain.StatusActive {
		t.Fatalf("map entry mismatch: %+v", byKey[0])
	}

	// The paged inspect view resolves the entry through the key join.
	items, total, err := maps.ListByPrincipal(ctx, principalID, 0, 0)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("list by principal: %v (%d/%d)", err, len(items), total)
	}
	if items[0].KeyID != k.ID {
		t.Fatalf("principal join mismatch: %+v", items[0])
	}
}

func TestMapCreateWithProvisionConflictRollsBackTheKey(t *testing.T) {
	db := pgtest.NewDB(t)
	ctx := context.Background()
	keys := adapter.NewRepository(db)

	principalID := seedMapPrincipal(t, ctx, db)

	// First issuance lands key + map row.
	k1 := newKey(t, principalID, "first")
	e1, _ := provisiondomain.NewIdentityMap(k1.ID, "litellm", "ref-first", []byte("enc"), mapTime)
	if err := keys.CreateWithProvision(ctx, k1, e1); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// A key-hash conflict (same secret reused) must roll the whole
	// transaction back: the second key row AND its map row both die.
	shared := keydomain.KeySecret("lk-shared-secret-body-0123456789abcdefgh")
	k2, err := keydomain.NewLoomingKey(uuid.NewString(), principalID, "second", shared, []byte("sealed-2"), mapTime)
	if err != nil {
		t.Fatalf("key 2: %v", err)
	}
	e2, _ := provisiondomain.NewIdentityMap(k2.ID, "litellm", "ref-second", []byte("enc"), mapTime)
	if err := keys.CreateWithProvision(ctx, k2, e2); err != nil {
		t.Fatalf("second create: %v", err)
	}
	k2dup, _ := keydomain.NewLoomingKey(uuid.NewString(), principalID, "dup-of-second", shared, []byte("sealed-3"), mapTime)
	e2dup, _ := provisiondomain.NewIdentityMap(k2dup.ID, "litellm", "ref-dup", []byte("enc"), mapTime)
	if err := keys.CreateWithProvision(ctx, k2dup, e2dup); !errors.Is(err, keydomain.ErrConflict) {
		t.Fatalf("want the key-hash conflict, got %v", err)
	}
	var keyRows, mapRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM loom_keys WHERE principal_id = $1`, principalID).Scan(&keyRows); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM identity_map m JOIN loom_keys k ON k.id = m.key_id WHERE k.principal_id = $1`, principalID).Scan(&mapRows); err != nil {
		t.Fatalf("count map rows: %v", err)
	}
	if keyRows != 2 || mapRows != 2 {
		t.Fatalf("the conflicting insert must roll back entirely, got %d keys / %d map rows", keyRows, mapRows)
	}

	// A (key_id, engine) map conflict must also roll the key insert
	// back: the map unique index is the idempotency anchor, and its
	// violation may not leave a key-only row behind.
	k3 := newKey(t, principalID, "third")
	e3, _ := provisiondomain.NewIdentityMap(k1.ID, "litellm", "ref-third", []byte("enc"), mapTime)
	if err := keys.CreateWithProvision(ctx, k3, e3); !errors.Is(err, provisiondomain.ErrConflict) {
		t.Fatalf("want the (key_id, engine) conflict, got %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM loom_keys WHERE principal_id = $1`, principalID).Scan(&keyRows); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if keyRows != 2 {
		t.Fatalf("the map conflict must roll the key insert back, got %d keys", keyRows)
	}
}

func TestMapDeleteIsIdempotent(t *testing.T) {
	db := pgtest.NewDB(t)
	ctx := context.Background()
	keys := adapter.NewRepository(db)
	maps := provisionadapter.NewMapRepository(db)

	principalID := seedMapPrincipal(t, ctx, db)
	k := newKey(t, principalID, "gone")
	entry, _ := provisiondomain.NewIdentityMap(k.ID, "litellm", "ref-gone", []byte("enc"), mapTime)
	if err := keys.CreateWithProvision(ctx, k, entry); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := maps.Delete(ctx, k.ID, "litellm"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := maps.Delete(ctx, k.ID, "litellm"); err != nil {
		t.Fatalf("a second delete must be a no-op, got %v", err)
	}
	items, err := maps.ListByKey(ctx, k.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("the entry must stay gone: %v (%d)", err, len(items))
	}
}

func TestMapKeyCascadeDeletesEntries(t *testing.T) {
	db := pgtest.NewDB(t)
	ctx := context.Background()
	keys := adapter.NewRepository(db)
	maps := provisionadapter.NewMapRepository(db)

	principalID := seedMapPrincipal(t, ctx, db)
	k := newKey(t, principalID, "cascade")
	entry, _ := provisiondomain.NewIdentityMap(k.ID, "litellm", "ref-cascade", []byte("enc"), mapTime)
	if err := keys.CreateWithProvision(ctx, k, entry); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM loom_keys WHERE id = $1`, k.ID); err != nil {
		t.Fatalf("delete key: %v", err)
	}
	items, err := maps.ListByKey(ctx, k.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("ON DELETE CASCADE must reap the map entries: %v (%d)", err, len(items))
	}
}

func TestMapListByPrincipalPagination(t *testing.T) {
	db := pgtest.NewDB(t)
	ctx := context.Background()
	keys := adapter.NewRepository(db)
	maps := provisionadapter.NewMapRepository(db)

	principalID := seedMapPrincipal(t, ctx, db)
	for i := 0; i < 5; i++ {
		k := newKey(t, principalID, string(rune('a'+i)))
		entry, _ := provisiondomain.NewIdentityMap(k.ID, "litellm", "ref-"+string(rune('a'+i)), []byte("enc"), mapTime.Add(time.Duration(i)*time.Minute))
		if err := keys.CreateWithProvision(ctx, k, entry); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	items, total, err := maps.ListByPrincipal(ctx, principalID, 2, 1)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if total != 5 || len(items) != 2 {
		t.Fatalf("page mismatch: %d items of %d", len(items), total)
	}
	// Newest first: the offset skips the newest row.
	if items[0].CreatedAt.Before(items[1].CreatedAt) {
		t.Fatalf("newest-first ordering violated: %+v", items)
	}
}
