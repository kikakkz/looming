// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"errors"
	"fmt"
	"time"
)

// Artifact-facing sentinels. Missing-artifact is a real state, not an
// adapter detail: the converge pipeline's render-diff branches on it
// (no stored artifact for (host, kind) = first render = must ship).
var (
	// ErrNoArtifact marks a read for a (host, kind) pair that has no
	// stored artifact yet.
	ErrNoArtifact = errors.New("topology: no rendered artifact")
	// ErrInvalidArtifact marks an artifact built without its identity
	// triple (host, kind, content).
	ErrInvalidArtifact = errors.New("topology: artifact requires host, kind, and content")
	// ErrUnknownArtifactKind marks a kind outside the phase-1
	// vocabulary.
	ErrUnknownArtifactKind = errors.New("topology: unknown artifact kind")
)

// ArtifactKindCompose is the phase-1 (only) artifact kind: a host's
// rendered compose file — the converged unit of deployment.
const ArtifactKindCompose = "compose"

// RenderArtifact is one rendered, hash-stored artifact: the last
// content of kind (e.g. the compose file) shipped to a host's
// executor, with the sha256 of that content for render-diff compares.
// Operator-facing host ids (topology.yaml strings), not uuids — the
// render_artifacts table is the operator key space (migration 0002).
type RenderArtifact struct {
	HostID      string
	Kind        string
	ContentHash string
	Content     string
	RenderedAt  time.Time
}

// NewRenderArtifact validates and builds an artifact at the boundary.
// The hash is computed by the renderer, not here — hashing is a render
// concern; the domain only demands a non-empty, consistent triple.
func NewRenderArtifact(hostID, kind, contentHash, content string, now time.Time) (RenderArtifact, error) {
	if hostID == "" || kind == "" || content == "" || contentHash == "" {
		return RenderArtifact{}, fmt.Errorf("%w: empty field", ErrInvalidArtifact)
	}
	if kind != ArtifactKindCompose {
		return RenderArtifact{}, fmt.Errorf("%w: %q", ErrUnknownArtifactKind, kind)
	}
	return RenderArtifact{
		HostID:      hostID,
		Kind:        kind,
		ContentHash: contentHash,
		Content:     content,
		RenderedAt:  now,
	}, nil
}
