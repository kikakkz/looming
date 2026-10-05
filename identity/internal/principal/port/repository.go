// SPDX-License-Identifier: Apache-2.0

// Package port defines the principal capability's persistence seams.
// Implementations are driven adapters (postgres in this component).
package port

import (
	"context"
	"time"

	"github.com/kikakkz/looming/identity/internal/principal/domain"
)

// Repository persists principals. Error contract: domain.ErrNotFound
// for missing rows, domain.ErrUsernameTaken on username uniqueness
// violation (Create), domain.ErrConflict when an optimistic-concurrency
// guard rejects the write (UpdateStatus).
type Repository interface {
	// Create inserts a new principal. A duplicate username fails with
	// domain.ErrUsernameTaken.
	Create(ctx context.Context, p *domain.Principal) error
	// ByID returns the principal or domain.ErrNotFound.
	ByID(ctx context.Context, id string) (*domain.Principal, error)
	// ByUsername returns the principal or domain.ErrNotFound.
	ByUsername(ctx context.Context, username string) (*domain.Principal, error)
	// List returns up to limit principals, skipping the first offset in
	// stable creation order, plus the total count for pagination.
	List(ctx context.Context, limit, offset int) (items []*domain.Principal, total int64, err error)
	// UpdateStatus persists a status transition made by a domain method
	// on p. The write is guarded by p.Version (optimistic concurrency):
	// a stale read fails with domain.ErrConflict. On success the
	// returned principal carries the bumped version.
	UpdateStatus(ctx context.Context, p *domain.Principal) (*domain.Principal, error)
	// Count returns the total number of principals.
	Count(ctx context.Context) (int64, error)
}

// InviteRepository persists invite tokens. Only hashes are stored.
type InviteRepository interface {
	// Create stores a new invite.
	Create(ctx context.Context, t *domain.InviteToken) error
	// ByHash returns the invite or domain.ErrNotFound.
	ByHash(ctx context.Context, hash []byte) (*domain.InviteToken, error)
	// MarkUsed records the consumption at usedAt. The update is guarded
	// by used_at IS NULL: a raced or repeated consume fails with
	// domain.ErrConflict, making consumption exactly-once under
	// concurrency.
	MarkUsed(ctx context.Context, hash []byte, usedAt time.Time) error
}
