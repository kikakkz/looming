// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the topology capability's driven
// implementations: the postgres store.
package adapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kikakkz/looming/topology/internal/topology/domain"
	"github.com/kikakkz/looming/topology/internal/topology/port"
)

// Store persists the topology snapshot in postgres: the singleton row
// and, in the same transaction, the placement set it replaces.
type Store struct {
	db *sql.DB
}

// NewStore wires the store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

var _ port.Store = (*Store)(nil)

// placementDoc is the placements.config jsonb payload: the named
// ports, the free-form config, and the compose host aliases of one
// placement. ExtraHosts is omitempty so rows written before the field
// existed (ports+config only) still decode.
type placementDoc struct {
	Ports      map[string]int    `json:"ports"`
	Config     map[string]string `json:"config"`
	ExtraHosts []string          `json:"extra_hosts,omitempty"`
}

// isUniqueViolation reports whether err is a postgres 23505 — the
// schema-level backstop for the placement (component, host) uniqueness
// invariant.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Save persists a topology transition atomically. The singleton row is
// upserted under the optimistic-revision guard (stored revision must be
// t.Revision-1; the bump lands in SQL) and the placement set is
// replaced wholesale. A lost race, or a placement pair colliding with a
// concurrent writer, surfaces as domain.ErrConflict /
// domain.ErrDuplicatePlacement; a collision inside t's own set is a
// domain-validation bypass and surfaces as domain.ErrDuplicatePlacement.
func (s *Store) Save(ctx context.Context, t domain.Topology) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("topology: save begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO topology (id, access_mode, access_transport, access_endpoint, revision, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE
		SET access_mode     = EXCLUDED.access_mode,
		    access_transport = EXCLUDED.access_transport,
		    access_endpoint = EXCLUDED.access_endpoint,
		    revision        = EXCLUDED.revision,
		    updated_at      = EXCLUDED.updated_at
		WHERE topology.revision = $5 - 1`,
		t.ID, string(t.Access.Mode), string(t.Access.Transport), string(t.Access.Endpoint), t.Revision, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("topology: save upsert: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("topology: save upsert affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrConflict
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM placements`); err != nil {
		return fmt.Errorf("topology: save clear placements: %w", err)
	}
	for _, p := range t.Placements {
		payload, err := json.Marshal(placementDoc{Ports: p.Ports, Config: p.Config, ExtraHosts: p.ExtraHosts})
		if err != nil {
			return fmt.Errorf("topology: save encode placement %q on %q: %w", p.Component, p.HostID, err)
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO placements (id, component, host_id, config)
			 VALUES ($1, $2, $3, $4)`,
			uuid.NewString(), p.Component, p.HostID, payload)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %q on host %q", domain.ErrDuplicatePlacement, p.Component, p.HostID)
			}
			return fmt.Errorf("topology: save placement %q on %q: %w", p.Component, p.HostID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("topology: save commit: %w", err)
	}
	return nil
}

// Current returns the persisted topology with its placement set in
// stable (component, host) order, or domain.ErrNoTopology when nothing
// has been declared yet.
func (s *Store) Current(ctx context.Context) (domain.Topology, error) {
	var t domain.Topology
	t.ID = domain.SingletonID
	err := s.db.QueryRowContext(ctx,
		`SELECT access_mode, access_transport, access_endpoint, revision, updated_at
		   FROM topology WHERE id = $1`, domain.SingletonID).
		Scan(&t.Access.Mode, &t.Access.Transport, &t.Access.Endpoint, &t.Revision, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Topology{}, domain.ErrNoTopology
	}
	if err != nil {
		return domain.Topology{}, fmt.Errorf("topology: current: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT component, host_id, config FROM placements ORDER BY component, host_id`)
	if err != nil {
		return domain.Topology{}, fmt.Errorf("topology: current placements: %w", err)
	}
	defer func() { _ = rows.Close() }()

	t.Placements = []domain.ComponentPlacement{}
	for rows.Next() {
		var (
			p       domain.ComponentPlacement
			payload []byte
		)
		if err := rows.Scan(&p.Component, &p.HostID, &payload); err != nil {
			return domain.Topology{}, fmt.Errorf("topology: current placements scan: %w", err)
		}
		var doc placementDoc
		if err := json.Unmarshal(payload, &doc); err != nil {
			return domain.Topology{}, fmt.Errorf("topology: current placements decode %q on %q: %w", p.Component, p.HostID, err)
		}
		p.Ports, p.Config, p.ExtraHosts = doc.Ports, doc.Config, doc.ExtraHosts
		t.Placements = append(t.Placements, p)
	}
	if err := rows.Err(); err != nil {
		return domain.Topology{}, fmt.Errorf("topology: current placements iterate: %w", err)
	}
	return t, nil
}
