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

	authnadapter "github.com/kikakkz/looming/identity/internal/authn/adapter"
	authndomain "github.com/kikakkz/looming/identity/internal/authn/domain"
	principaladapter "github.com/kikakkz/looming/identity/internal/principal/adapter"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

var integTime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func seedPrincipal(t *testing.T, db *sql.DB, username, password string, status principaldomain.Status) *principaldomain.Principal {
	t.Helper()
	repo := principaladapter.NewRepository(db)
	hash, err := authnadapter.Argon2idHasher{}.Hash(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	p := &principaldomain.Principal{
		ID:           uuid.NewString(),
		Username:     username,
		Kind:         principaldomain.KindHuman,
		DisplayName:  username,
		PasswordHash: hash,
		Status:       status,
		Roles:        []string{principaldomain.RoleMember, principaldomain.RoleAdmin},
		Version:      1,
		CreatedAt:    integTime,
		UpdatedAt:    integTime,
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	return p
}

func TestVerifyPassword(t *testing.T) {
	db := pgtest.NewDB(t)
	seedPrincipal(t, db, "ker", "correct horse battery", principaldomain.StatusActive)
	provider := authnadapter.NewLocalProvider(db, time.Hour, newSeqRand(), func() time.Time { return integTime })
	ctx := context.Background()

	id, err := provider.VerifyPassword(ctx, "ker", "correct horse battery")
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if id == "" {
		t.Fatal("principal id must be returned")
	}
	for _, tc := range []struct {
		name     string
		username string
		password string
	}{
		{"wrong password", "ker", "wrong horse battery"},
		{"unknown user", "mallory", "correct horse battery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := provider.VerifyPassword(ctx, tc.username, tc.password); !errors.Is(err, authndomain.ErrInvalidCredential) {
				t.Fatalf("want ErrInvalidCredential, got %v", err)
			}
		})
	}
}

func TestVerifyPasswordNonActiveDenied(t *testing.T) {
	db := pgtest.NewDB(t)
	seedPrincipal(t, db, "pending-user", "correct horse battery", principaldomain.StatusPending)
	provider := authnadapter.NewLocalProvider(db, time.Hour, newSeqRand(), func() time.Time { return integTime })
	if _, err := provider.VerifyPassword(context.Background(), "pending-user", "correct horse battery"); !errors.Is(err, authndomain.ErrInvalidCredential) {
		t.Fatalf("pending principal must not authenticate, got %v", err)
	}
}

func TestTokenIssueValidateRevoke(t *testing.T) {
	db := pgtest.NewDB(t)
	p := seedPrincipal(t, db, "ker", "correct horse battery", principaldomain.StatusActive)
	provider := authnadapter.NewLocalProvider(db, time.Hour, newSeqRand(), func() time.Time { return integTime })
	ctx := context.Background()

	raw, err := provider.Issue(ctx, p.ID)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	info, err := provider.Validate(ctx, raw)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if info.PrincipalID != p.ID {
		t.Fatalf("want principal %s, got %s", p.ID, info.PrincipalID)
	}
	if len(info.Roles) != 2 {
		t.Fatalf("roles must come from the principals row, got %v", info.Roles)
	}
	if !info.ExpiresAt.Equal(integTime.Add(time.Hour)) {
		t.Fatalf("expiry must follow the injected ttl, got %s", info.ExpiresAt)
	}

	if _, err := provider.Validate(ctx, "bogus-raw-token"); !errors.Is(err, authndomain.ErrInvalidCredential) {
		t.Fatalf("unknown token must fail closed, got %v", err)
	}

	if err := provider.Revoke(ctx, raw); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := provider.Validate(ctx, raw); !errors.Is(err, authndomain.ErrTokenRevoked) {
		t.Fatalf("revoked token must fail with ErrTokenRevoked, got %v", err)
	}
	if err := provider.Revoke(ctx, raw); err != nil {
		t.Fatalf("double revoke must be idempotent, got %v", err)
	}
	if err := provider.Revoke(ctx, "bogus-raw-token"); !errors.Is(err, authndomain.ErrInvalidCredential) {
		t.Fatalf("revoking an unknown token must fail, got %v", err)
	}
}

func TestValidateDisabledPrincipalFailsClosed(t *testing.T) {
	db := pgtest.NewDB(t)
	p := seedPrincipal(t, db, "ker", "correct horse battery", principaldomain.StatusActive)
	provider := authnadapter.NewLocalProvider(db, time.Hour, newSeqRand(), func() time.Time { return integTime })
	ctx := context.Background()
	raw, err := provider.Issue(ctx, p.ID)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	repo := principaladapter.NewRepository(db)
	stored, err := repo.ByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	stored.Status = principaldomain.StatusDisabled
	if _, err := repo.UpdateStatus(ctx, stored); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := provider.Validate(ctx, raw); !errors.Is(err, authndomain.ErrInvalidCredential) {
		t.Fatalf("disabled principal's token must fail closed, got %v", err)
	}
}

// newSeqRand returns a deterministic reader (integration layer needs
// shape, not unpredictability).
func newSeqRand() *seqRand { return &seqRand{} }

type seqRand struct{ n byte }

func (s *seqRand) Read(p []byte) (int, error) {
	for i := range p {
		s.n++
		p[i] = s.n
	}
	return len(p), nil
}
