// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the principal capability's driven
// implementations: the postgres repositories.
package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"

	"github.com/kikakkz/looming/identity/internal/principal/domain"
	"github.com/kikakkz/looming/identity/internal/principal/port"
)

// Repository persists principals in postgres.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

var _ port.Repository = (*Repository)(nil)

const principalCols = `id, username, kind, display_name, COALESCE(password_hash, ''), status, roles, version, created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanPrincipal(row scanner) (*domain.Principal, error) {
	var p domain.Principal
	if err := row.Scan(&p.ID, &p.Username, &p.Kind, &p.DisplayName, &p.PasswordHash,
		&p.Status, pq.Array(&p.Roles), &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// Create inserts a principal. The username uniqueness invariant maps to
// domain.ErrUsernameTaken (DO NOTHING + zero affected rows detects the
// conflict without parsing driver errors).
func (r *Repository) Create(ctx context.Context, p *domain.Principal) error {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO principals
		    (id, username, kind, display_name, password_hash, status, roles, version, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, $10)
		 ON CONFLICT (username) DO NOTHING`,
		p.ID, p.Username, p.Kind, p.DisplayName, p.PasswordHash, p.Status,
		pq.Array(p.Roles), p.Version, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("identity: principal create: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("identity: principal create affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrUsernameTaken
	}
	return nil
}

// ByID returns the principal or domain.ErrNotFound.
func (r *Repository) ByID(ctx context.Context, id string) (*domain.Principal, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+principalCols+` FROM principals WHERE id = $1`, id)
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity: principal by id: %w", err)
	}
	return p, nil
}

// ByUsername returns the principal or domain.ErrNotFound.
func (r *Repository) ByUsername(ctx context.Context, username string) (*domain.Principal, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+principalCols+` FROM principals WHERE username = $1`, username)
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity: principal by username: %w", err)
	}
	return p, nil
}

// List returns a page in stable (created_at, id) order plus the total.
func (r *Repository) List(ctx context.Context, limit, offset int) ([]*domain.Principal, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM principals`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("identity: principal count: %w", err)
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+principalCols+` FROM principals ORDER BY created_at, id LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("identity: principal list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := []*domain.Principal{}
	for rows.Next() {
		p, err := scanPrincipal(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("identity: principal list scan: %w", err)
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("identity: principal list iterate: %w", err)
	}
	return items, total, nil
}

// UpdateStatus persists a domain-transitioned principal. The optimistic
// version guard rejects stale reads with domain.ErrConflict; the bump
// happens in SQL (version = version + 1) and the fresh row is returned.
func (r *Repository) UpdateStatus(ctx context.Context, p *domain.Principal) (*domain.Principal, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE principals SET status = $1, updated_at = $2, version = version + 1
		  WHERE id = $3 AND version = $4`,
		p.Status, p.UpdatedAt, p.ID, p.Version)
	if err != nil {
		return nil, fmt.Errorf("identity: principal update status: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("identity: principal update status affected: %w", err)
	}
	if affected == 0 {
		return nil, domain.ErrConflict
	}
	return r.ByID(ctx, p.ID)
}

// Count returns the total number of principals.
func (r *Repository) Count(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM principals`).Scan(&n); err != nil {
		return 0, fmt.Errorf("identity: principal count: %w", err)
	}
	return n, nil
}
