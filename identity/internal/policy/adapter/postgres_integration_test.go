// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kikakkz/looming/identity/internal/policy/adapter"
	"github.com/kikakkz/looming/identity/internal/policy/domain"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

func TestPolicyStoreSingleton(t *testing.T) {
	store := adapter.NewStore(pgtest.NewDB(t))
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get seeded policy: %v", err)
	}
	if got.Mode != domain.ModeAdminOnly {
		t.Fatalf("migration must seed admin-only, got %s", got.Mode)
	}
	if got.ID != domain.SingletonID {
		t.Fatalf("singleton id must be %q, got %q", domain.SingletonID, got.ID)
	}

	first, err := domain.New(domain.ModeInvite, "admin-1", now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := store.Set(ctx, first); err != nil {
		t.Fatalf("Set: %v", err)
	}
	second, err := domain.New(domain.ModeSelfRegisterWithApproval, "admin-2", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := store.Set(ctx, second); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}

	got, err = store.Get(ctx)
	if err != nil {
		t.Fatalf("Get after overwrites: %v", err)
	}
	if got.Mode != domain.ModeSelfRegisterWithApproval || got.UpdatedBy != "admin-2" {
		t.Fatalf("overwrite must win, got %+v", got)
	}
}

func TestPolicyStoreNotFound(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewStore(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM registration_policy`); err != nil {
		t.Fatalf("delete policy row: %v", err)
	}
	if _, err := store.Get(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound on empty table, got %v", err)
	}
}
