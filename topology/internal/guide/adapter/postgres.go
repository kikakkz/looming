// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kikakkz/looming/topology/internal/guide/domain"
	"github.com/kikakkz/looming/topology/internal/guide/port"
)

// Store persists the Guide aggregate in the guide table (T0's schema:
// the singleton row with its snapshot jsonb and render stamp).
type Store struct {
	db *sql.DB
}

// NewStore wires the store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

var _ port.Store = (*Store)(nil)

// Current returns the persisted guide, or domain.ErrNoGuide when the
// table is empty — the pre-first-apply window.
func (s *Store) Current(ctx context.Context) (domain.Guide, error) {
	var (
		g       domain.Guide
		payload []byte
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT snapshot, rendered_rev, rendered_at FROM guide WHERE id = $1`, domain.SingletonID).
		Scan(&payload, &g.RenderedRev, &g.RenderedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Guide{}, domain.ErrNoGuide
	}
	if err != nil {
		return domain.Guide{}, fmt.Errorf("guide: current: %w", err)
	}
	if err := json.Unmarshal(payload, &g.Snapshot); err != nil {
		return domain.Guide{}, fmt.Errorf("guide: decode snapshot: %w", err)
	}
	g.ID = domain.SingletonID
	return g, nil
}

// Save upserts the singleton guide row, replacing any previous snapshot
// — re-rendering is a converge, not a history.
func (s *Store) Save(ctx context.Context, g domain.Guide) error {
	payload, err := json.Marshal(g.Snapshot)
	if err != nil {
		return fmt.Errorf("guide: encode snapshot: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO guide (id, snapshot, rendered_rev, rendered_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (id) DO UPDATE
		 SET snapshot     = EXCLUDED.snapshot,
		     rendered_rev = EXCLUDED.rendered_rev,
		     rendered_at  = EXCLUDED.rendered_at`,
		domain.SingletonID, payload, g.RenderedRev, g.RenderedAt)
	if err != nil {
		return fmt.Errorf("guide: save: %w", err)
	}
	return nil
}
