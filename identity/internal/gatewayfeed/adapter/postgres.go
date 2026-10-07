// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the gateway feed's driven implementation: the
// postgres read model. It reads the principal, key, and identity_map
// tables via its own SQL — the feed is a projection, not any aggregate
// (the authn-adapter precedent).
package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kikakkz/looming/identity/internal/gatewayfeed/domain"
	"github.com/kikakkz/looming/identity/internal/gatewayfeed/port"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	"github.com/lib/pq"
)

// Store is the feed's postgres read model.
type Store struct {
	db         *sql.DB
	engineName string
	opener     port.CredentialOpener
}

// NewStore wires the store. engineName selects the deployment's engine
// rows from identity_map (empty when no engine is configured: the join
// then matches nothing). opener unseals the projected credentials; nil
// leaves EngineCredential empty.
func NewStore(db *sql.DB, engineName string, opener port.CredentialOpener) *Store {
	return &Store{db: db, engineName: engineName, opener: opener}
}

var _ port.Store = (*Store)(nil)

// ListKeys returns the keys of active principals only — a disabled or
// pending principal's keys vanish from the feed (fail closed). Revoked
// keys stay listed with their status so syncers can distinguish delete
// from never-present, but never carry a credential: the join predicate
// restricts credentials to active keys, and revocation deletes the map
// row first, so a credential only ever rides an active key row.
func (s *Store) ListKeys(ctx context.Context) ([]domain.Key, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT k.key_hash, k.principal_id, k.status, m.credential_enc
		   FROM loom_keys k
		   JOIN principals p ON p.id = k.principal_id
		   LEFT JOIN identity_map m
		     ON m.key_id = k.id
		    AND m.engine = $1
		    AND m.status = 'active'
		    AND k.status = 'active'
		  WHERE p.status = 'active'
		  ORDER BY k.created_at, k.id`,
		s.engineName)
	if err != nil {
		return nil, fmt.Errorf("identity: gateway feed keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.Key{}
	for rows.Next() {
		var (
			k   domain.Key
			enc sql.Null[[]byte]
		)
		if err := rows.Scan(&k.Hash, &k.PrincipalID, &k.Status, &enc); err != nil {
			return nil, fmt.Errorf("identity: gateway feed key scan: %w", err)
		}
		if enc.Valid && s.opener != nil {
			raw, err := s.opener.Open(enc.V)
			if err != nil {
				// One unreadable credential must not take down the whole
				// feed: the key row still lists, without its credential.
				slog.ErrorContext(ctx, "identity: gateway feed credential unseal failed; omitting",
					"principal_id", k.PrincipalID, "err", err)
			} else {
				k.EngineCredential = string(raw)
			}
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
// existed" without a second round-trip. Each row carries the
// principal's effective permissions (union over builtin role bundles)
// so the gateway's model-permission projection needs no second source.
func (s *Store) ListPrincipals(ctx context.Context) ([]domain.Principal, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, status, roles FROM principals ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("identity: gateway feed principals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.Principal{}
	for rows.Next() {
		var (
			p     domain.Principal
			roles []string
		)
		if err := rows.Scan(&p.ID, &p.Status, pq.Array(&roles)); err != nil {
			return nil, fmt.Errorf("identity: gateway feed principal scan: %w", err)
		}
		for _, perm := range principaldomain.EffectivePermissions(roles) {
			p.Permissions = append(p.Permissions, string(perm))
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
