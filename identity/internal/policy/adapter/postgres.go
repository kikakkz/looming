// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the policy capability's driven
// implementations: the postgres singleton store.
package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kikakkz/looming/identity/internal/policy/domain"
	"github.com/kikakkz/looming/identity/internal/policy/port"
)

// Store persists the registration policy singleton.
type Store struct {
	db *sql.DB
}

// NewStore wires the store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

var _ port.Store = (*Store)(nil)

// Get returns the active policy or domain.ErrNotFound. Callers (the app
// service) fail closed into the admin-only default on the latter.
func (s *Store) Get(ctx context.Context) (*domain.Policy, error) {
	var p domain.Policy
	err := s.db.QueryRowContext(ctx,
		`SELECT id, mode, updated_by, updated_at FROM registration_policy WHERE id = $1`,
		domain.SingletonID,
	).Scan(&p.ID, &p.Mode, &p.UpdatedBy, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity: policy get: %w", err)
	}
	return &p, nil
}

// Set overwrites the singleton row; the fixed PK is what makes
// exactly-one-active structural (identity-l1 §4).
func (s *Store) Set(ctx context.Context, p *domain.Policy) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO registration_policy (id, mode, updated_by, updated_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (id) DO UPDATE
		 SET mode = EXCLUDED.mode, updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at`,
		p.ID, p.Mode, p.UpdatedBy, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("identity: policy set: %w", err)
	}
	return nil
}
