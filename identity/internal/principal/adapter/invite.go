// SPDX-License-Identifier: Apache-2.0
package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kikakkz/looming/identity/internal/principal/domain"
	"github.com/kikakkz/looming/identity/internal/principal/port"
)

// InviteRepository persists invite tokens in postgres.
type InviteRepository struct {
	db *sql.DB
}

// NewInviteRepository wires the repository.
func NewInviteRepository(db *sql.DB) *InviteRepository {
	return &InviteRepository{db: db}
}

var _ port.InviteRepository = (*InviteRepository)(nil)

// Create stores a new invite (hash only, never the raw token).
func (r *InviteRepository) Create(ctx context.Context, t *domain.InviteToken) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO invite_tokens (token_hash, created_by, created_at, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		t.TokenHash, t.CreatedBy, t.CreatedAt, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("identity: invite create: %w", err)
	}
	return nil
}

// ByHash returns the invite or domain.ErrNotFound.
func (r *InviteRepository) ByHash(ctx context.Context, hash []byte) (*domain.InviteToken, error) {
	var t domain.InviteToken
	var usedAt sql.NullTime
	err := r.db.QueryRowContext(ctx,
		`SELECT token_hash, created_by, created_at, expires_at, used_at
		   FROM invite_tokens WHERE token_hash = $1`, hash,
	).Scan(&t.TokenHash, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity: invite by hash: %w", err)
	}
	if usedAt.Valid {
		t.UsedAt = &usedAt.Time
	}
	return &t, nil
}

// MarkUsed records the consumption guarded by used_at IS NULL: a raced
// or repeated consume affects no row and fails with domain.ErrConflict,
// making consumption exactly-once under concurrency.
func (r *InviteRepository) MarkUsed(ctx context.Context, hash []byte, usedAt time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE invite_tokens SET used_at = $1 WHERE token_hash = $2 AND used_at IS NULL`,
		usedAt, hash)
	if err != nil {
		return fmt.Errorf("identity: invite mark used: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("identity: invite mark used affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrConflict
	}
	return nil
}
