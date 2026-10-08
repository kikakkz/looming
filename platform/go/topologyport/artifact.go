// SPDX-License-Identifier: Apache-2.0

package port

import (
	"context"

	domain "github.com/kikakkz/looming/platform/go/topologydomain"
)

// ArtifactStore persists rendered artifacts (migration 0002's
// render_artifacts) — the content + hash the converge pipeline ships
// per host. Error contract: domain.ErrNoArtifact when (host, kind) has
// no stored row.
type ArtifactStore interface {
	// Current returns the stored artifact for (hostID, kind), or
	// domain.ErrNoArtifact when nothing has been rendered there yet.
	Current(ctx context.Context, hostID, kind string) (domain.RenderArtifact, error)
	// Save upserts the artifact for (a.HostID, a.Kind): re-rendering a
	// host replaces the previous content and hash in place.
	Save(ctx context.Context, a domain.RenderArtifact) error
}
