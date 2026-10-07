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

func TestPrincipalRepositoryBlocksDisablingLastActiveAdmin(t *testing.T) {
	repo := adapter.NewRepository(pgtest.NewDB(t))
	ctx := context.Background()
	admin := newPrincipal("root", domain.StatusActive)
	admin.Roles = []string{domain.RoleAdmin}
	if err := repo.Create(ctx, admin); err != nil {
		t.Fatalf("Create: %v", err)
	}
	member := newPrincipal("ker", domain.StatusActive)
	if err := repo.Create(ctx, member); err != nil {
		t.Fatalf("Create member: %v", err)
	}

	// The sole active admin cannot be disabled — atomically.
	admin.Status = domain.StatusDisabled
	if _, err := repo.UpdateStatus(ctx, admin); !errors.Is(err, domain.ErrLastAdmin) {
		t.Fatalf("want ErrLastAdmin, got %v", err)
	}
	// A member is not an admin: disabling them is unaffected.
	member.Status = domain.StatusDisabled
	if _, err := repo.UpdateStatus(ctx, member); err != nil {
		t.Fatalf("disabling a member must succeed: %v", err)
	}
	// A second active admin unlocks the guard.
	backup := newPrincipal("root2", domain.StatusActive)
	backup.Roles = []string{domain.RoleAdmin}
	if err := repo.Create(ctx, backup); err != nil {
		t.Fatalf("Create backup admin: %v", err)
	}
	updated, err := repo.UpdateStatus(ctx, admin)
	if err != nil {
		t.Fatalf("disable with a second active admin must succeed: %v", err)
	}
	if updated.Status != domain.StatusDisabled {
		t.Fatalf("want disabled, got %s", updated.Status)
	}
}

func TestPrincipalRepositorySetRoles(t *testing.T) {
	repo := adapter.NewRepository(pgtest.NewDB(t))
	ctx := context.Background()
	admin := newPrincipal("root", domain.StatusActive)
	admin.Roles = []string{domain.RoleAdmin}
	if err := repo.Create(ctx, admin); err != nil {
		t.Fatalf("Create: %v", err)
	}
	member := newPrincipal("ker", domain.StatusActive)
	if err := repo.Create(ctx, member); err != nil {
		t.Fatalf("Create member: %v", err)
	}

	// Happy path: promote the member to admin+member.
	member.Roles = []string{domain.RoleMember, domain.RoleAdmin}
	updated, err := repo.SetRoles(ctx, member)
	if err != nil {
		t.Fatalf("SetRoles: %v", err)
	}
	if len(updated.Roles) != 2 {
		t.Fatalf("want two roles, got %v", updated.Roles)
	}
	member = updated

	// Strip admin from a principal while another active admin exists.
	member.Roles = []string{domain.RoleMember}
	if _, err := repo.SetRoles(ctx, member); err != nil {
		t.Fatalf("strip with backup admin must succeed: %v", err)
	}

	// The sole active admin cannot be stripped — atomically.
	admin.Roles = []string{domain.RoleMember}
	if _, err := repo.SetRoles(ctx, admin); !errors.Is(err, domain.ErrLastAdmin) {
		t.Fatalf("want ErrLastAdmin, got %v", err)
	}

	// Stale versions are rejected.
	fresh := newPrincipal("fresh", domain.StatusActive)
	if err := repo.Create(ctx, fresh); err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	fresh.Version = 99
	fresh.Roles = []string{domain.RoleAdmin}
	if _, err := repo.SetRoles(ctx, fresh); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want ErrConflict for stale version, got %v", err)
	}
}

func TestInviteRepositoryLifecycle(t *testing.T) {
	repo := adapter.NewInviteRepository(pgtest.NewDB(t))
	ctx := context.Background()
	_, tok, err := domain.GenerateInvite("admin-1", time.Hour, newDeterministicRand(1), testTime)
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
// The seed keeps distinct invites from colliding on token_hash.
type deterministicRand struct{ buf []byte }

func (d *deterministicRand) Read(p []byte) (int, error) {
	copy(p, d.buf)
	return len(p), nil
}

func newDeterministicRand(seed byte) *deterministicRand {
	buf := make([]byte, 32)
	buf[0] = seed
	return &deterministicRand{buf: buf}
}

func TestInviteRepositorySourceAndEmailRoundTrip(t *testing.T) {
	repo := adapter.NewInviteRepository(pgtest.NewDB(t))
	ctx := context.Background()

	// Admin invite: source defaults to 'admin', no email binding.
	_, adminTok, err := domain.GenerateInvite("admin-1", time.Hour, newDeterministicRand(2), testTime)
	if err != nil {
		t.Fatalf("GenerateInvite: %v", err)
	}
	if err := repo.Create(ctx, adminTok); err != nil {
		t.Fatalf("Create admin invite: %v", err)
	}
	got, err := repo.ByHash(ctx, adminTok.TokenHash)
	if err != nil {
		t.Fatalf("ByHash admin invite: %v", err)
	}
	if got.Source != domain.InviteSourceAdmin || got.Email != "" {
		t.Fatalf("admin invite must roundtrip source %q and empty email, got %q/%q",
			domain.InviteSourceAdmin, got.Source, got.Email)
	}

	// Bootstrap invite: source and bound email persist.
	_, bootTok, err := domain.GenerateBootstrapInvite("ops@example.com", time.Hour, newDeterministicRand(3), testTime)
	if err != nil {
		t.Fatalf("GenerateBootstrapInvite: %v", err)
	}
	if err := repo.Create(ctx, bootTok); err != nil {
		t.Fatalf("Create bootstrap invite: %v", err)
	}
	got, err = repo.ByHash(ctx, bootTok.TokenHash)
	if err != nil {
		t.Fatalf("ByHash bootstrap invite: %v", err)
	}
	if got.Source != domain.InviteSourceBootstrap || got.Email != "ops@example.com" {
		t.Fatalf("bootstrap invite must roundtrip source+email, got %q/%q", got.Source, got.Email)
	}
}

func TestInviteRepositoryExistsBySource(t *testing.T) {
	repo := adapter.NewInviteRepository(pgtest.NewDB(t))
	ctx := context.Background()

	exists, err := repo.ExistsBySource(ctx, domain.InviteSourceBootstrap)
	if err != nil || exists {
		t.Fatalf("fresh database: want (false, nil), got (%v, %v)", exists, err)
	}
	_, adminTok, err := domain.GenerateInvite("admin-1", time.Hour, newDeterministicRand(4), testTime)
	if err != nil {
		t.Fatalf("GenerateInvite: %v", err)
	}
	if err := repo.Create(ctx, adminTok); err != nil {
		t.Fatalf("Create admin invite: %v", err)
	}
	exists, err = repo.ExistsBySource(ctx, domain.InviteSourceBootstrap)
	if err != nil || exists {
		t.Fatalf("admin-only database: want (false, nil), got (%v, %v)", exists, err)
	}
	_, bootTok, err := domain.GenerateBootstrapInvite("ops@example.com", time.Hour, newDeterministicRand(5), testTime)
	if err != nil {
		t.Fatalf("GenerateBootstrapInvite: %v", err)
	}
	if err := repo.Create(ctx, bootTok); err != nil {
		t.Fatalf("Create bootstrap invite: %v", err)
	}
	exists, err = repo.ExistsBySource(ctx, domain.InviteSourceBootstrap)
	if err != nil || !exists {
		t.Fatalf("after bootstrap mint: want (true, nil), got (%v, %v)", exists, err)
	}
	exists, err = repo.ExistsBySource(ctx, domain.InviteSourceAdmin)
	if err != nil || !exists {
		t.Fatalf("admin source must be found too, want (true, nil), got (%v, %v)", exists, err)
	}
}

func TestInviteRepositoryBootstrapOneShotEnforcedByIndex(t *testing.T) {
	repo := adapter.NewInviteRepository(pgtest.NewDB(t))
	ctx := context.Background()

	_, first, err := domain.GenerateBootstrapInvite("ops@example.com", time.Hour, newDeterministicRand(6), testTime)
	if err != nil {
		t.Fatalf("GenerateBootstrapInvite: %v", err)
	}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("Create first bootstrap invite: %v", err)
	}
	// The partial unique index backstops the app-level window check: a
	// concurrent second mint fails with ErrConflict (mapped to 409
	// bootstrap_closed at the app layer).
	_, second, err := domain.GenerateBootstrapInvite("second@example.com", time.Hour, newDeterministicRand(7), testTime)
	if err != nil {
		t.Fatalf("GenerateBootstrapInvite second: %v", err)
	}
	if err := repo.Create(ctx, second); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second bootstrap mint must fail with ErrConflict, got %v", err)
	}
	// Admin invites are unaffected by the bootstrap one-shot index.
	for i, seed := range []byte{8, 9} {
		_, adminTok, err := domain.GenerateInvite("admin-1", time.Hour, newDeterministicRand(seed), testTime.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("GenerateInvite: %v", err)
		}
		if err := repo.Create(ctx, adminTok); err != nil {
			t.Fatalf("admin invite %d must still be mintable: %v", i, err)
		}
	}
}

func TestPrincipalRepositoryCreateWithInviteConsume(t *testing.T) {
	// Each subtest boots its own database: the partial unique index
	// enforces one bootstrap voucher per database, so the scenarios
	// cannot share a container.
	newBootstrapInvite := func(t *testing.T, email string, seed byte) (*adapter.Repository, *adapter.InviteRepository, *domain.InviteToken) {
		t.Helper()
		db := pgtest.NewDB(t)
		repo := adapter.NewRepository(db)
		invites := adapter.NewInviteRepository(db)
		_, tok, err := domain.GenerateBootstrapInvite(email, time.Hour, newDeterministicRand(seed), testTime)
		if err != nil {
			t.Fatalf("GenerateBootstrapInvite: %v", err)
		}
		if err := invites.Create(context.Background(), tok); err != nil {
			t.Fatalf("invite Create: %v", err)
		}
		return repo, invites, tok
	}

	t.Run("consume and create land together", func(t *testing.T) {
		repo, invites, tok := newBootstrapInvite(t, "first@example.com", 10)
		ctx := context.Background()
		p := newPrincipal("root-one", domain.StatusActive)
		if err := repo.CreateWithInviteConsume(ctx, p, tok.TokenHash, testTime.Add(time.Minute)); err != nil {
			t.Fatalf("CreateWithInviteConsume: %v", err)
		}
		stored, err := invites.ByHash(ctx, tok.TokenHash)
		if err != nil {
			t.Fatalf("ByHash: %v", err)
		}
		if stored.UsedAt == nil {
			t.Fatal("the invite must be consumed by the atomic registration")
		}
	})

	t.Run("lost consume race fails without a principal", func(t *testing.T) {
		repo, invites, tok := newBootstrapInvite(t, "second@example.com", 11)
		ctx := context.Background()
		if err := invites.MarkUsed(ctx, tok.TokenHash, testTime); err != nil {
			t.Fatalf("pre-consume: %v", err)
		}
		p := newPrincipal("root-two", domain.StatusActive)
		err := repo.CreateWithInviteConsume(ctx, p, tok.TokenHash, testTime.Add(time.Minute))
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("want ErrConflict for an already-used invite, got %v", err)
		}
		if _, err := repo.ByUsername(ctx, "root-two"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("a lost race must not create a principal, got %v", err)
		}
	})

	t.Run("username conflict keeps the voucher consumable", func(t *testing.T) {
		repo, invites, tok := newBootstrapInvite(t, "third@example.com", 12)
		ctx := context.Background()
		if err := repo.Create(ctx, newPrincipal("root-three", domain.StatusActive)); err != nil {
			t.Fatalf("seed username: %v", err)
		}
		conflict := newPrincipal("root-three", domain.StatusActive)
		err := repo.CreateWithInviteConsume(ctx, conflict, tok.TokenHash, testTime.Add(time.Minute))
		if !errors.Is(err, domain.ErrUsernameTaken) {
			t.Fatalf("want ErrUsernameTaken, got %v", err)
		}
		stored, err := invites.ByHash(ctx, tok.TokenHash)
		if err != nil {
			t.Fatalf("ByHash: %v", err)
		}
		if stored.UsedAt != nil {
			t.Fatal("a rolled-back insert must leave the voucher unconsumed")
		}
		// The retried registration with a fresh username consumes it.
		retry := newPrincipal("root-three-b", domain.StatusActive)
		if err := repo.CreateWithInviteConsume(ctx, retry, tok.TokenHash, testTime.Add(2*time.Minute)); err != nil {
			t.Fatalf("retry after username conflict: %v", err)
		}
	})

	t.Run("insert failure rolls the consume back", func(t *testing.T) {
		repo, invites, tok := newBootstrapInvite(t, "fourth@example.com", 13)
		ctx := context.Background()
		broken := newPrincipal("root-four", domain.StatusActive)
		broken.ID = "not-a-uuid" // rejected by the uuid column after the consume UPDATE
		err := repo.CreateWithInviteConsume(ctx, broken, tok.TokenHash, testTime.Add(time.Minute))
		if err == nil || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrUsernameTaken) {
			t.Fatalf("the invalid insert must surface as its own error, got %v", err)
		}
		stored, err := invites.ByHash(ctx, tok.TokenHash)
		if err != nil {
			t.Fatalf("ByHash: %v", err)
		}
		if stored.UsedAt != nil {
			t.Fatal("a failed insert must roll the consume back — the voucher is the only recovery path")
		}
		if _, err := repo.ByUsername(ctx, "root-four"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("no principal may survive a rolled-back registration, got %v", err)
		}
	})

	t.Run("unreachable database surfaces its error", func(t *testing.T) {
		repo, _, tok := newBootstrapInvite(t, "fifth@example.com", 14)
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		err := repo.CreateWithInviteConsume(canceled, newPrincipal("root-five", domain.StatusActive), tok.TokenHash, testTime)
		if err == nil || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrUsernameTaken) {
			t.Fatalf("a dead database must surface as its own error, got %v", err)
		}
	})
}

func TestPrincipalRepositoryExistsAdmin(t *testing.T) {
	repo := adapter.NewRepository(pgtest.NewDB(t))
	ctx := context.Background()

	exists, err := repo.ExistsAdmin(ctx)
	if err != nil || exists {
		t.Fatalf("empty table: want (false, nil), got (%v, %v)", exists, err)
	}
	member := newPrincipal("ker", domain.StatusActive)
	if err := repo.Create(ctx, member); err != nil {
		t.Fatalf("Create member: %v", err)
	}
	exists, err = repo.ExistsAdmin(ctx)
	if err != nil || exists {
		t.Fatalf("member-only table: want (false, nil), got (%v, %v)", exists, err)
	}
	admin := newPrincipal("root", domain.StatusPending)
	admin.Roles = []string{domain.RoleAdmin}
	if err := repo.Create(ctx, admin); err != nil {
		t.Fatalf("Create admin: %v", err)
	}
	// The window rule counts any principal carrying the admin role,
	// regardless of status — a pending admin still closed it.
	exists, err = repo.ExistsAdmin(ctx)
	if err != nil || !exists {
		t.Fatalf("pending admin must still exist for the window, want (true, nil), got (%v, %v)", exists, err)
	}
}
