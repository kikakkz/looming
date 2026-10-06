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

	"github.com/kikakkz/looming/identity/internal/quota/adapter"
	"github.com/kikakkz/looming/identity/internal/quota/domain"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

var quotaTime = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func seedQuotaPrincipal(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO principals (id, username, kind, status, roles) VALUES ($1, 'quota-user', 'human', 'active', '{member}')`, id); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	return id
}

func TestQuotaUpsertAndReadRoundTrip(t *testing.T) {
	db := pgtest.NewDB(t)
	ctx := context.Background()
	repo := adapter.NewRepository(db)

	principalID := seedQuotaPrincipal(t, ctx, db)

	// Unset is the unlimited default and a first-class 404 state.
	if _, err := repo.ByPrincipal(ctx, principalID); !errors.Is(err, domain.ErrNoQuota) {
		t.Fatalf("want ErrNoQuota, got %v", err)
	}

	q, err := domain.NewQuota(principalID, 500, domain.UnitUSD, 7, "admin-1", quotaTime)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if err := repo.Upsert(ctx, q); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.ByPrincipal(ctx, principalID)
	if err != nil {
		t.Fatalf("by principal: %v", err)
	}
	if got.PrincipalID != principalID || got.Amount != 500 || got.Unit != domain.UnitUSD ||
		got.WindowDays != 7 || got.UpdatedBy != "admin-1" || !got.UpdatedAt.Equal(quotaTime) {
		t.Fatalf("quota round trip mismatch: %+v", got)
	}

	// Upsert replaces; the row stays one-per-principal.
	q2, _ := domain.NewQuota(principalID, 0, domain.UnitTokens, 1, "admin-2", quotaTime.Add(time.Hour))
	if err := repo.Upsert(ctx, q2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err = repo.ByPrincipal(ctx, principalID)
	if err != nil {
		t.Fatalf("by principal after replace: %v", err)
	}
	if got.Amount != 0 || got.Unit != domain.UnitTokens || got.WindowDays != 1 || got.UpdatedBy != "admin-2" {
		t.Fatalf("replace mismatch: %+v", got)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota WHERE principal_id = $1`, principalID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("one row per principal, got %d", rows)
	}
}

func TestQuotaUpsertForeignKeyGuardsGhostPrincipal(t *testing.T) {
	db := pgtest.NewDB(t)
	ctx := context.Background()
	repo := adapter.NewRepository(db)

	q, err := domain.NewQuota(uuid.NewString(), 5, domain.UnitUSD, 7, "admin-1", quotaTime)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if err := repo.Upsert(ctx, q); err == nil {
		t.Fatal("a quota for a principal that does not exist must fail at the foreign key")
	}
}
