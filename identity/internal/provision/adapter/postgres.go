// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the IdentityMap's postgres read/delete side.
// Creation rides the key repository's issuance transaction; this
// adapter owns what revocation and the admin inspect surface need.
package adapter

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kikakkz/looming/identity/internal/provision/domain"
	"github.com/kikakkz/looming/identity/internal/provision/port"
)

// MapRepository persists IdentityMap entries in postgres.
type MapRepository struct {
	db *sql.DB
}

// NewMapRepository wires the repository.
func NewMapRepository(db *sql.DB) *MapRepository {
	return &MapRepository{db: db}
}

var _ port.MapRepository = (*MapRepository)(nil)

const mapCols = `key_id, engine, credential_ref, credential_enc, status, created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanMap(row scanner) (*domain.IdentityMap, error) {
	var m domain.IdentityMap
	if err := row.Scan(&m.KeyID, &m.Engine, &m.CredentialRef, &m.CredentialEnc,
		&m.Status, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

// ListByKey returns every map entry of one LoomingKey.
func (r *MapRepository) ListByKey(ctx context.Context, keyID string) ([]*domain.IdentityMap, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+mapCols+` FROM identity_map WHERE key_id = $1 ORDER BY created_at, engine`, keyID)
	if err != nil {
		return nil, fmt.Errorf("identity: map list by key: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*domain.IdentityMap{}
	for rows.Next() {
		m, err := scanMap(rows)
		if err != nil {
			return nil, fmt.Errorf("identity: map list by key scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity: map list by key iterate: %w", err)
	}
	return out, nil
}

// Delete removes the (key, engine) entry; a missing row is not an error
// (revocation is one-way and best-effort at the engine).
func (r *MapRepository) Delete(ctx context.Context, keyID, engine string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM identity_map WHERE key_id = $1 AND engine = $2`, keyID, engine)
	if err != nil {
		return fmt.Errorf("identity: map delete: %w", err)
	}
	return nil
}

// ListByPrincipal pages the principal's entries through their keys,
// newest first; limit <= 0 disables the limit.
func (r *MapRepository) ListByPrincipal(ctx context.Context, principalID string, limit, offset int) ([]*domain.IdentityMap, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM identity_map m
		   JOIN loom_keys k ON k.id = m.key_id
		  WHERE k.principal_id = $1`, principalID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("identity: map count by principal: %w", err)
	}
	query := `SELECT m.key_id, m.engine, m.credential_ref, m.credential_enc, m.status, m.created_at, m.updated_at
	   FROM identity_map m
	   JOIN loom_keys k ON k.id = m.key_id
	  WHERE k.principal_id = $1
	  ORDER BY m.created_at DESC, m.key_id, m.engine`
	args := []any{principalID}
	if limit > 0 {
		query += ` LIMIT $2 OFFSET $3`
		args = append(args, limit, offset)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("identity: map list by principal: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*domain.IdentityMap{}
	for rows.Next() {
		m, err := scanMap(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("identity: map list by principal scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("identity: map list by principal iterate: %w", err)
	}
	return out, total, nil
}
