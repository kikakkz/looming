// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the host capability's driven implementations:
// the postgres registry.
package adapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	domain "github.com/kikakkz/looming/platform/go/hostdomain"
	port "github.com/kikakkz/looming/platform/go/hostport"
)

// Registry persists hosts in postgres.
type Registry struct {
	db *sql.DB
}

// NewRegistry wires the registry.
func NewRegistry(db *sql.DB) *Registry {
	return &Registry{db: db}
}

var _ port.Registry = (*Registry)(nil)

const hostCols = `id, address, role_labels, credential_hash, joined_at, capabilities`

type scanner interface {
	Scan(dest ...any) error
}

// scanCapabilities unmarshals the nullable jsonb column: NULL means
// the host joined without facts (an old CLI) and stays nil.
func scanCapabilities(raw sql.Null[[]byte]) (*domain.Capabilities, error) {
	if !raw.Valid {
		return nil, nil
	}
	var caps domain.Capabilities
	if err := json.Unmarshal(raw.V, &caps); err != nil {
		return nil, fmt.Errorf("topology: host capabilities decode: %w", err)
	}
	return &caps, nil
}

func scanHost(row scanner) (*domain.Host, error) {
	var (
		h    domain.Host
		caps sql.Null[[]byte]
	)
	if err := row.Scan(&h.ID, &h.Address, pq.Array(&h.RoleLabels), &h.CredentialHash, &h.JoinedAt, &caps); err != nil {
		return nil, err
	}
	var err error
	if h.Capabilities, err = scanCapabilities(caps); err != nil {
		return nil, err
	}
	return &h, nil
}

// capabilitiesJSON marshals the facts for the jsonb column; a host
// without observed facts writes SQL NULL, keeping old-CLI rows and
// new-CLI rows indistinguishable except by the column's presence.
func capabilitiesJSON(h *domain.Host) ([]byte, error) {
	if h.Capabilities == nil {
		return nil, nil
	}
	raw, err := json.Marshal(h.Capabilities)
	if err != nil {
		return nil, fmt.Errorf("topology: host capabilities encode: %w", err)
	}
	return raw, nil
}

// nonNilLabels keeps the NOT NULL DEFAULT '{}' column happy when the
// domain value carries no labels at all.
func nonNilLabels(labels []string) []string {
	if labels == nil {
		return []string{}
	}
	return labels
}

// isUniqueViolation reports whether err is a postgres 23505, the
// schema-level backstop for the address-uniqueness invariant.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Register upserts by address (the port contract). The insert uses ON
// CONFLICT DO NOTHING; the zero-row outcome is classified by reading
// the occupying row — same ID means idempotent re-registration
// (labels and credential refreshed, join time kept), a different ID is
// the uniqueness violation.
func (r *Registry) Register(ctx context.Context, h *domain.Host) (*domain.Host, error) {
	caps, err := capabilitiesJSON(h)
	if err != nil {
		return nil, err
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO hosts (id, address, role_labels, credential_hash, joined_at, capabilities)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (address) DO NOTHING`,
		h.ID, h.Address, pq.Array(nonNilLabels(h.RoleLabels)), h.CredentialHash, h.JoinedAt, caps)
	if err != nil {
		return nil, fmt.Errorf("topology: host register: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("topology: host register affected: %w", err)
	}
	if affected == 1 {
		return h, nil
	}

	occupant, err := r.ByAddress(ctx, h.Address)
	if err != nil {
		return nil, err
	}
	if occupant.ID != h.ID {
		return nil, fmt.Errorf("%w: %q held by host %q", domain.ErrAddressTaken, h.Address, occupant.ID)
	}

	// Idempotent re-registration of the same host: refresh the mutable
	// fields — labels, credential, and the freshly observed facts — keep
	// the original join time. Capabilities use COALESCE: an old CLI's
	// re-registering join carries no facts and must not erase the stored
	// ones (nil keeps the current value, the re-join address contract).
	if _, err := r.db.ExecContext(ctx,
		`UPDATE hosts SET role_labels = $2, credential_hash = $3, capabilities = COALESCE($4, capabilities) WHERE id = $1`,
		h.ID, pq.Array(nonNilLabels(h.RoleLabels)), h.CredentialHash, caps); err != nil {
		return nil, fmt.Errorf("topology: host re-register: %w", err)
	}
	return r.ByID(ctx, h.ID)
}

// List returns every registered host in id order.
func (r *Registry) List(ctx context.Context) ([]domain.Host, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+hostCols+` FROM hosts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("topology: host list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var hosts []domain.Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, fmt.Errorf("topology: host list scan: %w", err)
		}
		hosts = append(hosts, *h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("topology: host list: %w", err)
	}
	return hosts, nil
}

// ByID returns the host or domain.ErrNotFound.
func (r *Registry) ByID(ctx context.Context, id string) (*domain.Host, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+hostCols+` FROM hosts WHERE id = $1`, id)
	h, err := scanHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("topology: host by id: %w", err)
	}
	return h, nil
}

// ByAddress returns the host or domain.ErrNotFound.
func (r *Registry) ByAddress(ctx context.Context, address string) (*domain.Host, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+hostCols+` FROM hosts WHERE address = $1`, address)
	h, err := scanHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("topology: host by address: %w", err)
	}
	return h, nil
}

// Update persists an address/label change for the host identified by
// h.ID. The address move is guarded by the table's UNIQUE constraint:
// a 23505 there is the uniqueness invariant, mapped to
// domain.ErrAddressTaken. The credential and capabilities slots are
// not written here — the join flow owns both.
func (r *Registry) Update(ctx context.Context, h *domain.Host) (*domain.Host, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE hosts SET address = $2, role_labels = $3 WHERE id = $1`,
		h.ID, h.Address, pq.Array(nonNilLabels(h.RoleLabels)))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %q", domain.ErrAddressTaken, h.Address)
		}
		return nil, fmt.Errorf("topology: host update: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("topology: host update affected: %w", err)
	}
	if affected == 0 {
		return nil, fmt.Errorf("%w: id %q", domain.ErrNotFound, h.ID)
	}
	return r.ByID(ctx, h.ID)
}
