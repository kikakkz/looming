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
	"github.com/kikakkz/looming/identity/internal/key/domain"
	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

var testTime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestKeyRepositoryRoundTrip(t *testing.T) {
	db := pgtest.NewDB(t)
	repo := adapter.NewRepository(db)
	ctx := context.Background()

	principalID := uuid.NewString()
	mustCreatePrincipal(t, ctx, db, principalID)

	secret, _ := domain.GenerateKeySecret(newDeterministicRand())
	k, err := domain.NewLoomingKey(uuid.NewString(), principalID, "ci", secret, []byte("sealed"), testTime)
	if err != nil {
		t.Fatalf("NewLoomingKey: %v", err)
	}
	if err := repo.Create(ctx, k); err != nil {
		t.Fatalf("Create: %v", err)
	}

	byID, err := repo.ByID(ctx, k.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if byID.PrincipalID != principalID || byID.Name != "ci" || byID.Status != domain.StatusActive {
		t.Fatalf("roundtrip mismatch: %+v", byID)
	}
	if byID.KeyHash != k.KeyHash || string(byID.Sealed) != "sealed" {
		t.Fatal("dual-track columns must roundtrip")
	}
	if byID.Prefix != k.Prefix || byID.Last4 != k.Last4 {
		t.Fatalf("display columns mismatch: %+v", byID)
	}
	if byID.RevokedAt != nil {
		t.Fatal("fresh key must not be revoked")
	}

	byHash, err := repo.ByHash(ctx, secret.Hash())
	if err != nil {
		t.Fatalf("ByHash: %v", err)
	}
	if byHash.ID != k.ID {
		t.Fatalf("ByHash resolved %s, want %s", byHash.ID, k.ID)
	}
}

func TestKeyRepositoryRejectsDuplicateHash(t *testing.T) {
	db := pgtest.NewDB(t)
	repo := adapter.NewRepository(db)
	ctx := context.Background()
	principalID := uuid.NewString()
	mustCreatePrincipal(t, ctx, db, principalID)

	secret, _ := domain.GenerateKeySecret(newDeterministicRand())
	k, _ := domain.NewLoomingKey(uuid.NewString(), principalID, "", secret, []byte("sealed"), testTime)
	if err := repo.Create(ctx, k); err != nil {
		t.Fatalf("Create: %v", err)
	}
	dup, _ := domain.NewLoomingKey(uuid.NewString(), principalID, "dup", secret, []byte("sealed"), testTime)
	if err := repo.Create(ctx, dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate hash must fail with ErrConflict, got %v", err)
	}
}

func TestKeyRepositoryNotFound(t *testing.T) {
	db := pgtest.NewDB(t)
	repo := adapter.NewRepository(db)
	ctx := context.Background()
	if _, err := repo.ByID(ctx, uuid.NewString()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ByID: want ErrNotFound, got %v", err)
	}
	if _, err := repo.ByHash(ctx, [32]byte{1}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ByHash: want ErrNotFound, got %v", err)
	}
}

func TestKeyRepositoryListByPrincipalScopesAndOrders(t *testing.T) {
	db := pgtest.NewDB(t)
	repo := adapter.NewRepository(db)
	ctx := context.Background()
	owner := uuid.NewString()
	other := uuid.NewString()
	mustCreatePrincipal(t, ctx, db, owner)
	mustCreatePrincipal(t, ctx, db, other)

	for i, name := range []string{"one", "two", "three"} {
		secret, _ := domain.GenerateKeySecret(&deterministicRand{buf: []byte{byte(i + 1), byte(i + 2)}})
		k, _ := domain.NewLoomingKey(uuid.NewString(), owner, name, secret, []byte("sealed"), testTime.Add(time.Duration(i)*time.Minute))
		if err := repo.Create(ctx, k); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}
	foreign, _ := domain.GenerateKeySecret(&deterministicRand{buf: []byte{9, 9}})
	fk, _ := domain.NewLoomingKey(uuid.NewString(), other, "foreign", foreign, []byte("sealed"), testTime)
	if err := repo.Create(ctx, fk); err != nil {
		t.Fatalf("Create foreign: %v", err)
	}

	items, err := repo.ListByPrincipal(ctx, owner)
	if err != nil {
		t.Fatalf("ListByPrincipal: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 owner keys, got %d", len(items))
	}
	for i, name := range []string{"one", "two", "three"} {
		if items[i].Name != name {
			t.Fatalf("stable creation order violated at %d: %+v", i, items[i])
		}
	}
}

func TestKeyRepositoryRevokeIsOneWay(t *testing.T) {
	db := pgtest.NewDB(t)
	repo := adapter.NewRepository(db)
	ctx := context.Background()
	principalID := uuid.NewString()
	mustCreatePrincipal(t, ctx, db, principalID)

	secret, _ := domain.GenerateKeySecret(newDeterministicRand())
	k, _ := domain.NewLoomingKey(uuid.NewString(), principalID, "", secret, []byte("sealed"), testTime)
	if err := repo.Create(ctx, k); err != nil {
		t.Fatalf("Create: %v", err)
	}

	revokedAt := testTime.Add(time.Hour)
	if err := repo.Revoke(ctx, k.ID, revokedAt); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, err := repo.ByID(ctx, k.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if got.Status != domain.StatusRevoked || got.RevokedAt == nil || !got.RevokedAt.Equal(revokedAt) {
		t.Fatalf("revoke mismatch: %+v", got)
	}
	if err := repo.Revoke(ctx, k.ID, revokedAt.Add(time.Hour)); !errors.Is(err, domain.ErrAlreadyRevoked) {
		t.Fatalf("second revoke must be ErrAlreadyRevoked, got %v", err)
	}
	if err := repo.Revoke(ctx, uuid.NewString(), revokedAt); !errors.Is(err, domain.ErrAlreadyRevoked) {
		t.Fatalf("revoking a missing key must collapse to ErrAlreadyRevoked, got %v", err)
	}
}

func TestKeyRepositoryCountSinceWindows(t *testing.T) {
	db := pgtest.NewDB(t)
	repo := adapter.NewRepository(db)
	ctx := context.Background()
	principalID := uuid.NewString()
	mustCreatePrincipal(t, ctx, db, principalID)

	base := testTime
	for i := 0; i < 3; i++ {
		secret, _ := domain.GenerateKeySecret(&deterministicRand{buf: []byte{byte(i), byte(i * 2)}})
		k, _ := domain.NewLoomingKey(uuid.NewString(), principalID, "", secret, []byte("sealed"), base.Add(time.Duration(i)*time.Hour))
		if err := repo.Create(ctx, k); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	n, err := repo.CountSince(ctx, principalID, base.Add(-time.Minute))
	if err != nil || n != 3 {
		t.Fatalf("full window: want 3, got %d (%v)", n, err)
	}
	n, err = repo.CountSince(ctx, principalID, base.Add(time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("1h window: want 2, got %d (%v)", n, err)
	}
	n, err = repo.CountSince(ctx, principalID, base.Add(2*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("2h window: want 1, got %d (%v)", n, err)
	}
	n, err = repo.CountSince(ctx, principalID, base.Add(4*time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("past all keys: want 0, got %d (%v)", n, err)
	}
}

// mustCreatePrincipal inserts the FK row the loom_keys constraint
// requires; this suite tests the key repository, not principal writes.
func mustCreatePrincipal(t *testing.T, ctx context.Context, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO principals (id, username, kind, status, roles)
		 VALUES ($1, $2, 'human', 'active', '{member}')`, id, "user-"+id[:8]); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
}

// deterministicRand feeds GenerateKeySecret without crypto/rand: the
// integration layer needs token shape, not unpredictability.
type deterministicRand struct{ buf []byte }

func (d *deterministicRand) Read(p []byte) (int, error) {
	cycled := make([]byte, len(p))
	for i := range cycled {
		cycled[i] = d.buf[i%len(d.buf)]
	}
	copy(p, cycled)
	return len(p), nil
}

func newDeterministicRand() *deterministicRand { return &deterministicRand{buf: make([]byte, 32)} }

func TestKeyRepositoryCreateWithProvisionRoundTrip(t *testing.T) {
	db := pgtest.NewDB(t)
	repo := adapter.NewRepository(db)
	ctx := context.Background()

	principalID := uuid.NewString()
	mustCreatePrincipal(t, ctx, db, principalID)

	secret, _ := domain.GenerateKeySecret(newDeterministicRand())
	k, err := domain.NewLoomingKey(uuid.NewString(), principalID, "engine", secret, []byte("sealed"), testTime)
	if err != nil {
		t.Fatalf("NewLoomingKey: %v", err)
	}
	entry, err := provisiondomain.NewIdentityMap(k.ID, "litellm", "ref-engine", []byte("enc-engine"), testTime)
	if err != nil {
		t.Fatalf("NewIdentityMap: %v", err)
	}
	if err := repo.CreateWithProvision(ctx, k, entry); err != nil {
		t.Fatalf("CreateWithProvision: %v", err)
	}

	// The map row must reference the key and carry the sealed blob.
	var (
		mapKeyID string
		ref      string
		sealed   []byte
		status   string
	)
	if err := db.QueryRowContext(ctx,
		`SELECT key_id, credential_ref, credential_enc, status FROM identity_map WHERE key_id = $1`, k.ID).
		Scan(&mapKeyID, &ref, &sealed, &status); err != nil {
		t.Fatalf("map row: %v", err)
	}
	if mapKeyID != k.ID || ref != "ref-engine" || string(sealed) != "enc-engine" || status != "active" {
		t.Fatalf("map row mismatch: %s %s %s %s", mapKeyID, ref, sealed, status)
	}

	// The transaction's rollback path: a conflicting second call (same
	// key hash) must leave the first rows intact and add nothing.
	dup, _ := domain.NewLoomingKey(uuid.NewString(), principalID, "dup", secret, []byte("sealed"), testTime)
	dupEntry, _ := provisiondomain.NewIdentityMap(dup.ID, "litellm", "ref-dup", []byte("enc"), testTime)
	if err := repo.CreateWithProvision(ctx, dup, dupEntry); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want key-hash conflict, got %v", err)
	}
	var keyRows, mapRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM loom_keys WHERE principal_id = $1`, principalID).Scan(&keyRows); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM identity_map`).Scan(&mapRows); err != nil {
		t.Fatalf("count map rows: %v", err)
	}
	if keyRows != 1 || mapRows != 1 {
		t.Fatalf("the rolled-back insert must add nothing, got %d keys / %d map rows", keyRows, mapRows)
	}

	// A (key_id, engine) map conflict rolls the key insert back too —
	// the UNIQUE anchor is the idempotency guard.
	other, _ := domain.GenerateKeySecret(&deterministicRand{buf: []byte{7, 7}})
	k3, _ := domain.NewLoomingKey(uuid.NewString(), principalID, "third", other, []byte("sealed"), testTime)
	e3, _ := provisiondomain.NewIdentityMap(k.ID, "litellm", "ref-third", []byte("enc"), testTime)
	if err := repo.CreateWithProvision(ctx, k3, e3); !errors.Is(err, provisiondomain.ErrConflict) {
		t.Fatalf("want the (key_id, engine) conflict, got %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM loom_keys WHERE principal_id = $1`, principalID).Scan(&keyRows); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if keyRows != 1 {
		t.Fatalf("the map conflict must roll the key insert back, got %d keys", keyRows)
	}
}
