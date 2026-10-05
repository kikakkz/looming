// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the gateway feed's driven implementation: the
// postgres read model. It reads the principal and key tables via its
// own SQL — the feed is a projection, not either aggregate (the
// authn-adapter precedent).
package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kikakkz/looming/identity/internal/gatewayfeed/domain"
	"github.com/kikakkz/looming/identity/internal/gatewayfeed/port"
)

// Store is the feed's postgres read model.
type Store struct {
	db *sql.DB
}

// NewStore wires the store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

var _ port.Store = (*Store)(nil)

// ListKeys returns the keys of active principals only — a disabled or
// pending principal's keys vanish from the feed (fail closed). Revoked
// keys stay listed with their status so syncers can distinguish delete
// from never-present.
func (s *Store) ListKeys(ctx context.Context) ([]domain.Key, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT k.key_hash, k.principal_id, k.status
		   FROM loom_keys k
		   JOIN principals p ON p.id = k.principal_id
		  WHERE p.status = 'active'
		  ORDER BY k.created_at, k.id`)
	if err != nil {
		return nil, fmt.Errorf("identity: gateway feed keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.Key{}
	for rows.Next() {
		var k domain.Key
		if err := rows.Scan(&k.Hash, &k.PrincipalID, &k.Status); err != nil {
			return nil, fmt.Errorf("identity: gateway feed key scan: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity: gateway feed keys iterate: %w", err)
	}
	return out, nil
}

// ListPrincipals returns every principal and its status, pending and
// disabled included: consumers need to tell "disabled" from "never
// existed" without a second round-trip.
func (s *Store) ListPrincipals(ctx context.Context) ([]domain.Principal, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, status FROM principals ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("identity: gateway feed principals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.Principal{}
	for rows.Next() {
		var p domain.Principal
		if err := rows.Scan(&p.ID, &p.Status); err != nil {
			return nil, fmt.Errorf("identity: gateway feed principal scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity: gateway feed principals iterate: %w", err)
	}
	return out, nil
}

// ByHash resolves the validation digest joined with the principal
// status. A revoked key or a disabled/pending owner resolves with its
// real statuses — the service owns the fail-closed mapping so the
// policy lives in one place.
func (s *Store) ByHash(ctx context.Context, hash []byte) (domain.Key, string, error) {
	var (
		k               domain.Key
		principalStatus string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT k.key_hash, k.principal_id, k.status, p.status
		   FROM loom_keys k
		   JOIN principals p ON p.id = k.principal_id
		  WHERE k.key_hash = $1`, hash).
		Scan(&k.Hash, &k.PrincipalID, &k.Status, &principalStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Key{}, "", domain.ErrNotFound
	}
	if err != nil {
		return domain.Key{}, "", fmt.Errorf("identity: gateway feed by hash: %w", err)
	}
	return k, principalStatus, nil
}
