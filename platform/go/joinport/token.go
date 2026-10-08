// SPDX-License-Identifier: Apache-2.0

// Package port defines the join capability's persistence seam.
// Implementations are driven adapters (postgres in this component).
package port

import (
	"context"
	"time"

	domain "github.com/kikakkz/looming/platform/go/joindomain"
)

// TokenStore persists join tokens. Error contract: domain.ErrTokenNotFound
// for a missing ByHash; domain.ErrTokenUsed for a guarded MarkUsed that
// affected no row (already consumed or revoked — by a prior call or a
// racing one, making consumption exactly-once under concurrency).
// The port works in hashes only; raw tokens never reach it.
type TokenStore interface {
	// Create stores a freshly minted token (hash only).
	Create(ctx context.Context, t *domain.JoinToken) error
	// ByHash returns the token or domain.ErrTokenNotFound.
	ByHash(ctx context.Context, hash []byte) (*domain.JoinToken, error)
	// MarkUsed records consumption at usedAt guarded by used_at IS NULL:
	// a raced or repeated call affects no row and fails with
	// domain.ErrTokenUsed.
	MarkUsed(ctx context.Context, hash []byte, usedAt time.Time) error
	// List returns every token, newest first — the admin-side
	// `token list` view.
	List(ctx context.Context) ([]domain.JoinToken, error)
}
