// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	domain "github.com/kikakkz/looming/platform/go/topologydomain"
	port "github.com/kikakkz/looming/platform/go/topologyport"
)

// ArtifactStore persists rendered artifacts in the render_artifacts
// table (migration 0002). Operator-facing host ids, keyed by (host,
// kind) — see the migration's header for why the key is text.
type ArtifactStore struct {
	db *sql.DB
}

// NewArtifactStore wires the store.
func NewArtifactStore(db *sql.DB) *ArtifactStore {
	return &ArtifactStore{db: db}
}

var _ port.ArtifactStore = (*ArtifactStore)(nil)

// Current returns the stored artifact for (hostID, kind), or
// domain.ErrNoArtifact when the pair has never been saved.
func (s *ArtifactStore) Current(ctx context.Context, hostID, kind string) (domain.RenderArtifact, error) {
	var a domain.RenderArtifact
	err := s.db.QueryRowContext(ctx,
		`SELECT content_hash, content, rendered_at
		   FROM render_artifacts WHERE host_id = $1 AND kind = $2`,
		hostID, kind).
		Scan(&a.ContentHash, &a.Content, &a.RenderedAt)
	a.HostID, a.Kind = hostID, kind
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RenderArtifact{}, domain.ErrNoArtifact
	}
	if err != nil {
		return domain.RenderArtifact{}, fmt.Errorf("topology: artifact current %q/%q: %w", hostID, kind, err)
	}
	return a, nil
}

// Save upserts the artifact for (a.HostID, a.Kind), replacing any
// previous content and hash — re-rendering a host is a converge, not a
// history.
func (s *ArtifactStore) Save(ctx context.Context, a domain.RenderArtifact) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO render_artifacts (host_id, kind, content_hash, content, rendered_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (host_id, kind) DO UPDATE
		 SET content_hash = EXCLUDED.content_hash,
		     content      = EXCLUDED.content,
		     rendered_at  = EXCLUDED.rendered_at`,
		a.HostID, a.Kind, a.ContentHash, a.Content, a.RenderedAt)
	if err != nil {
		return fmt.Errorf("topology: artifact save %q/%q: %w", a.HostID, a.Kind, err)
	}
	return nil
}
