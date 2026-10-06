// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kikakkz/looming/identity/internal/quota/domain"
	"github.com/kikakkz/looming/identity/internal/quota/port"
)

// Repository persists Quotas in postgres.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

var _ port.Repository = (*Repository)(nil)

// Upsert inserts or replaces the principal's quota; the principal
// foreign key is the existence guard.
func (r *Repository) Upsert(ctx context.Context, q *domain.Quota) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO quota
		    (principal_id, amount, unit, window_days, updated_by, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (principal_id) DO UPDATE SET
		    amount = EXCLUDED.amount,
		    unit = EXCLUDED.unit,
		    window_days = EXCLUDED.window_days,
		    updated_by = EXCLUDED.updated_by,
		    updated_at = EXCLUDED.updated_at`,
		q.PrincipalID, q.Amount, q.Unit, q.WindowDays, q.UpdatedBy, q.UpdatedAt)
	if err != nil {
		return fmt.Errorf("identity: quota upsert: %w", err)
	}
	return nil
}

// ByPrincipal returns the quota or domain.ErrNoQuota (the unlimited
// default is a first-class state, not an empty object).
func (r *Repository) ByPrincipal(ctx context.Context, principalID string) (*domain.Quota, error) {
	var q domain.Quota
	err := r.db.QueryRowContext(ctx,
		`SELECT principal_id, amount, unit, window_days, updated_by, updated_at
		   FROM quota WHERE principal_id = $1`, principalID).
		Scan(&q.PrincipalID, &q.Amount, &q.Unit, &q.WindowDays, &q.UpdatedBy, &q.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNoQuota
	}
	if err != nil {
		return nil, fmt.Errorf("identity: quota by principal: %w", err)
	}
	return &q, nil
}
