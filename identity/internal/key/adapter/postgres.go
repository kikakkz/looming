// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kikakkz/looming/identity/internal/key/domain"
	"github.com/kikakkz/looming/identity/internal/key/port"
	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
)

// Repository persists LoomingKeys in postgres.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

var _ port.Repository = (*Repository)(nil)

const keyCols = `id, principal_id, name, prefix, last4, key_hash, key_enc, status, created_at, revoked_at`

type scanner interface {
	Scan(dest ...any) error
}

// execer abstracts *sql.DB and *sql.Tx so the insert shapes share one
// implementation inside and outside transactions.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// scanKey reads a row into the aggregate. The dual-track secret columns
// land in KeyHash and Sealed; both stay service-internal.
func scanKey(row scanner) (*domain.LoomingKey, error) {
	var (
		k       domain.LoomingKey
		hash    []byte
		revoked sql.NullTime
	)
	if err := row.Scan(&k.ID, &k.PrincipalID, &k.Name, &k.Prefix, &k.Last4,
		&hash, &k.Sealed, &k.Status, &k.CreatedAt, &revoked); err != nil {
		return nil, err
	}
	if len(hash) != len(k.KeyHash) {
		return nil, fmt.Errorf("identity: key hash column is %d bytes, want %d", len(hash), len(k.KeyHash))
	}
	copy(k.KeyHash[:], hash)
	if revoked.Valid {
		k.RevokedAt = &revoked.Time
	}
	return &k, nil
}

// Create inserts a key. The hash uniqueness invariant maps to
// domain.ErrConflict (DO NOTHING + zero affected rows detects the
// conflict without parsing driver errors — the principal repository's
// precedent).
func (r *Repository) Create(ctx context.Context, k *domain.LoomingKey) error {
	return insertKey(ctx, r.db, k)
}

func insertKey(ctx context.Context, ex execer, k *domain.LoomingKey) error {
	res, err := ex.ExecContext(ctx,
		`INSERT INTO loom_keys
		    (id, principal_id, name, prefix, last4, key_hash, key_enc, status, created_at, revoked_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (key_hash) DO NOTHING`,
		k.ID, k.PrincipalID, k.Name, k.Prefix, k.Last4, k.KeyHash[:], k.Sealed,
		k.Status, k.CreatedAt, k.RevokedAt)
	if err != nil {
		return fmt.Errorf("identity: key create: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("identity: key create affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrConflict
	}
	return nil
}

// insertMapEntry writes the provision aggregate's row. The map table
// lives in this adapter for one reason: the issuance transaction spans
// both tables, and a transaction may hold only one adapter's SQL
// (CreateWithInviteConsume precedent).
func insertMapEntry(ctx context.Context, ex execer, m *provisiondomain.IdentityMap) error {
	res, err := ex.ExecContext(ctx,
		`INSERT INTO identity_map
		    (key_id, engine, credential_ref, credential_enc, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (key_id, engine) DO NOTHING`,
		m.KeyID, m.Engine, m.CredentialRef, m.CredentialEnc, m.Status, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("identity: map entry create: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("identity: map entry create affected: %w", err)
	}
	if affected == 0 {
		return provisiondomain.ErrConflict
	}
	return nil
}

// CreateWithProvision inserts the key and its engine-credential map
// entry atomically: both rows persist or neither. The engine credential
// itself lives at the engine — a rollback here leaves nothing behind to
// clean up in identity.
func (r *Repository) CreateWithProvision(ctx context.Context, k *domain.LoomingKey, m *provisiondomain.IdentityMap) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("identity: key create with provision: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertKey(ctx, tx, k); err != nil {
		return err
	}
	if err := insertMapEntry(ctx, tx, m); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("identity: key create with provision: commit: %w", err)
	}
	return nil
}

// ByID returns the key or domain.ErrNotFound.
func (r *Repository) ByID(ctx context.Context, id string) (*domain.LoomingKey, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+keyCols+` FROM loom_keys WHERE id = $1`, id)
	k, err := scanKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity: key by id: %w", err)
	}
	return k, nil
}

// ByHash returns the key or domain.ErrNotFound. This is the gateway
// validate path's lookup.
func (r *Repository) ByHash(ctx context.Context, hash [32]byte) (*domain.LoomingKey, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+keyCols+` FROM loom_keys WHERE key_hash = $1`, hash[:])
	k, err := scanKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity: key by hash: %w", err)
	}
	return k, nil
}

// ListByPrincipal returns the principal's keys oldest-first.
func (r *Repository) ListByPrincipal(ctx context.Context, principalID string) ([]*domain.LoomingKey, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+keyCols+` FROM loom_keys WHERE principal_id = $1 ORDER BY created_at, id`, principalID)
	if err != nil {
		return nil, fmt.Errorf("identity: key list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := []*domain.LoomingKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("identity: key list scan: %w", err)
		}
		items = append(items, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity: key list iterate: %w", err)
	}
	return items, nil
}

// Revoke flips an active key to revoked exactly once. The guard
// collapses not-found and already-revoked into domain.ErrAlreadyRevoked
// — both are safe to expose, and the northbound contract wants one 409.
func (r *Repository) Revoke(ctx context.Context, id string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE loom_keys
		    SET status = $2, revoked_at = $3
		  WHERE id = $1 AND status = 'active'`,
		id, domain.StatusRevoked, now)
	if err != nil {
		return fmt.Errorf("identity: key revoke: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("identity: key revoke affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrAlreadyRevoked
	}
	return nil
}

// CountSince counts keys created at or after since — the rate-limit
// window probe.
func (r *Repository) CountSince(ctx context.Context, principalID string, since time.Time) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM loom_keys WHERE principal_id = $1 AND created_at >= $2`,
		principalID, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("identity: key count since: %w", err)
	}
	return n, nil
}
