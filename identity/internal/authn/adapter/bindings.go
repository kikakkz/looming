// SPDX-License-Identifier: Apache-2.0

// Package adapter — the OIDC binding store: the immutable
// issuer+subject → principal mapping (identity slice E). Resolving
// logins through the binding — never through username derivation —
// is the ownership guarantee: two IdP accounts whose derived
// usernames collide can never share a principal (CodeRabbit security
// review on PR #141).
package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kikakkz/looming/identity/internal/authn/port"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
)

// BindingRepository is the postgres BindingRepository.
type BindingRepository struct {
	db *sql.DB
}

// NewBindingRepository wires the store.
func NewBindingRepository(db *sql.DB) *BindingRepository {
	return &BindingRepository{db: db}
}

// ByIssuerSubject resolves the bound principal or
// principaldomain.ErrNotFound.
func (r *BindingRepository) ByIssuerSubject(ctx context.Context, issuer, subject string) (string, error) {
	var principalID string
	err := r.db.QueryRowContext(ctx,
		`SELECT principal_id FROM oidc_bindings WHERE issuer = $1 AND subject = $2`,
		issuer, subject).Scan(&principalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", principaldomain.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("identity: oidc binding lookup: %w", err)
	}
	return principalID, nil
}

// Create binds an issuer+subject pair to a principal. A raced
// duplicate fails with the driver's unique violation; the login flow
// treats that as a lost race and re-reads.
func (r *BindingRepository) Create(ctx context.Context, issuer, subject, principalID string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO oidc_bindings (issuer, subject, principal_id) VALUES ($1, $2, $3)`,
		issuer, subject, principalID)
	if err != nil {
		return fmt.Errorf("identity: oidc binding create: %w", err)
	}
	return nil
}

var _ port.BindingRepository = (*BindingRepository)(nil)
