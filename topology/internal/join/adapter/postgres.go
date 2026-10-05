// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kikakkz/looming/topology/internal/join/domain"
	"github.com/kikakkz/looming/topology/internal/join/port"
)

// TokenStore persists join tokens in postgres (the T0 join_tokens
// table: hash PK, role, created_by, expiry, used_at).
type TokenStore struct {
	db *sql.DB
}

// NewTokenStore wires the store.
func NewTokenStore(db *sql.DB) *TokenStore {
	return &TokenStore{db: db}
}

var _ port.TokenStore = (*TokenStore)(nil)

// Create stores a freshly minted token. A hash collision — a 256-bit
// space collision — surfaces as a plain error; nothing about the raw
// token is recoverable from it.
func (s *TokenStore) Create(ctx context.Context, t *domain.JoinToken) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO join_tokens (token_hash, role, created_by, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		t.TokenHash, t.Role, t.CreatedBy, t.ExpiresAt, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("topology: join token create: %w", err)
	}
	return nil
}

// ByHash returns the token or domain.ErrTokenNotFound.
func (s *TokenStore) ByHash(ctx context.Context, hash []byte) (*domain.JoinToken, error) {
	var t domain.JoinToken
	var usedAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT token_hash, role, created_by, created_at, expires_at, used_at
		   FROM join_tokens WHERE token_hash = $1`, hash).
		Scan(&t.TokenHash, &t.Role, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("topology: join token by hash: %w", err)
	}
	if usedAt.Valid {
		t.UsedAt = &usedAt.Time
	}
	return &t, nil
}

// MarkUsed records consumption guarded by used_at IS NULL — the
// slice-A invite-token precedent: a raced or repeated consume affects
// no row and fails with domain.ErrTokenUsed, making consumption
// exactly-once under concurrency.
func (s *TokenStore) MarkUsed(ctx context.Context, hash []byte, usedAt time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE join_tokens SET used_at = $1 WHERE token_hash = $2 AND used_at IS NULL`,
		usedAt, hash)
	if err != nil {
		return fmt.Errorf("topology: join token mark used: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("topology: join token mark used affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrTokenUsed
	}
	return nil
}

// List returns every token, newest first.
func (s *TokenStore) List(ctx context.Context) ([]domain.JoinToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token_hash, role, created_by, created_at, expires_at, used_at
		   FROM join_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("topology: join token list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.JoinToken
	for rows.Next() {
		var t domain.JoinToken
		var usedAt sql.NullTime
		if err := rows.Scan(&t.TokenHash, &t.Role, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &usedAt); err != nil {
			return nil, fmt.Errorf("topology: join token list scan: %w", err)
		}
		if usedAt.Valid {
			t.UsedAt = &usedAt.Time
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("topology: join token list iterate: %w", err)
	}
	return out, nil
}
