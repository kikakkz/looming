// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kikakkz/looming/identity/internal/principal/adapter"
	"github.com/kikakkz/looming/identity/internal/principal/domain"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

var testTime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func newPrincipal(username string, status domain.Status) *domain.Principal {
	return &domain.Principal{
		ID:           uuid.NewString(),
		Username:     username,
		Kind:         domain.KindHuman,
		DisplayName:  "Integration Test",
		PasswordHash: "argon2:fake",
		Status:       status,
		Roles:        []string{domain.RoleMember},
		Version:      1,
		CreatedAt:    testTime,
		UpdatedAt:    testTime,
	}
}

func TestPrincipalRepositoryRoundTrip(t *testing.T) {
	repo := adapter.NewRepository(pgtest.NewDB(t))
	ctx := context.Background()

	want := newPrincipal("ker", domain.StatusPending)
	if err := repo.Create(ctx, want); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.ByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if got.Username != want.Username || got.Status != want.Status || got.Version != 1 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if len(got.Roles) != 1 || got.Roles[0] != domain.RoleMember {
		t.Fatalf("roles must roundtrip, got %v", got.Roles)
	}
	byUsername, err := repo.ByUsername(ctx, "ker")
	if err != nil {
		t.Fatalf("ByUsername: %v", err)
	}
	if byUsername.ID != want.ID {
		t.Fatalf("ByUsername resolved %+v, want %s", byUsername, want.ID)
	}
}

func TestPrincipalRepositoryUsernameTaken(t *testing.T) {
	repo := adapter.NewRepository(pgtest.NewDB(t))
	ctx := context.Background()
	if err := repo.Create(ctx, newPrincipal("ker", domain.StatusActive)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := repo.Create(ctx, newPrincipal("ker", domain.StatusPending))
	if !errors.Is(err, domain.ErrUsernameTaken) {
		t.Fatalf("want ErrUsernameTaken, got %v", err)
	}
}

func TestPrincipalRepositoryNotFound(t *testing.T) {
	repo := adapter.NewRepository(pgtest.NewDB(t))
	ctx := context.Background()
	if _, err := repo.ByID(ctx, uuid.NewString()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ByID: want ErrNotFound, got %v", err)
	}
	if _, err := repo.ByUsername(ctx, "nobody"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ByUsername: want ErrNotFound, got %v", err)
	}
}

func TestPrincipalRepositoryListAndCount(t *testing.T) {
	repo := adapter.NewRepository(pgtest.NewDB(t))
	ctx := context.Background()
	for _, username := range []string{"u-one", "u-two", "u-three"} {
		if err := repo.Create(ctx, newPrincipal(username, domain.StatusActive)); err != nil {
			t.Fatalf("Create %s: %v", username, err)
		}
	}
	items, total, err := repo.List(ctx, 2, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(items) != 2 {
		t.Fatalf("want 2 items of 3, got %d of %d", len(items), total)
	}
	items, _, err = repo.List(ctx, 2, 2)
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item on page 2, got %d", len(items))
	}
	n, err := repo.Count(ctx)
	if err != nil || n != 3 {
		t.Fatalf("Count: want 3, got %d (%v)", n, err)
	}
}

func TestPrincipalRepositoryUpdateStatusOptimisticLock(t *testing.T) {
	repo := adapter.NewRepository(pgtest.NewDB(t))
	ctx := context.Background()
	created := newPrincipal("ker", domain.StatusPending)
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	created.Status = domain.StatusActive
	updated, err := repo.UpdateStatus(ctx, created)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if updated.Status != domain.StatusActive || updated.Version != 2 {
		t.Fatalf("want active v2, got %s v%d", updated.Status, updated.Version)
	}

	// A stale copy (version 1) must lose the race.
	stale := *created
	if _, err := repo.UpdateStatus(ctx, &stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want ErrConflict for stale version, got %v", err)
	}
}

func TestInviteRepositoryLifecycle(t *testing.T) {
	repo := adapter.NewInviteRepository(pgtest.NewDB(t))
	ctx := context.Background()
	_, tok, err := domain.GenerateInvite("admin-1", time.Hour, newDeterministicRand(), testTime)
	if err != nil {
		t.Fatalf("GenerateInvite: %v", err)
	}
	if err := repo.Create(ctx, tok); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.ByHash(ctx, tok.TokenHash)
	if err != nil {
		t.Fatalf("ByHash: %v", err)
	}
	if string(got.TokenHash) != string(tok.TokenHash) || got.CreatedBy != "admin-1" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.UsedAt != nil {
		t.Fatal("fresh invite must be unused")
	}
	if _, err := repo.ByHash(ctx, domain.HashToken("unknown")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := repo.MarkUsed(ctx, tok.TokenHash, testTime.Add(time.Minute)); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}
	used, err := repo.ByHash(ctx, tok.TokenHash)
	if err != nil {
		t.Fatalf("ByHash after use: %v", err)
	}
	if used.UsedAt == nil {
		t.Fatal("UsedAt must be set after MarkUsed")
	}
	if err := repo.MarkUsed(ctx, tok.TokenHash, testTime.Add(2*time.Minute)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("double consume must fail with ErrConflict, got %v", err)
	}
}

// deterministicRand feeds GenerateInvite without crypto/rand: the
// integration layer does not need token unpredictability, only shape.
type deterministicRand struct{ buf []byte }

func (d *deterministicRand) Read(p []byte) (int, error) {
	copy(p, d.buf)
	return len(p), nil
}

func newDeterministicRand() *deterministicRand {
	return &deterministicRand{buf: make([]byte, 32)}
}
