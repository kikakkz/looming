// SPDX-License-Identifier: Apache-2.0

// Package port defines the key capability's seams: the reversible
// sealer (dual-track storage's reveal half) and the persistence
// contract. Implementations are driven adapters (postgres, AES-GCM).
package port

import (
	"context"
	"time"

	"github.com/kikakkz/looming/identity/internal/key/domain"
)

// Sealer reversibly protects the raw secret for the reveal path
// (identity-l1 §2 dual-track storage). The adapter implements AES-GCM:
// a random 12-byte nonce prepended to the ciphertext. Domain code never
// touches this cipher material beyond storing the produced blob.
type Sealer interface {
	// Seal encrypts plaintext for at-rest storage.
	Seal(plaintext []byte) ([]byte, error)
	// Open decrypts a sealed blob. A tampered or foreign blob fails.
	Open(sealed []byte) ([]byte, error)
}

// Repository persists LoomingKeys. Error contract: domain.ErrNotFound
// for missing rows, domain.ErrConflict on key-hash uniqueness
// violation (Create), domain.ErrAlreadyRevoked for the one-way revoke
// guard (Revoke) — the guard collapses not-found and already-revoked
// into the same safe sentinel, mirroring the northbound contract.
type Repository interface {
	// Create inserts a new key. A duplicate key hash fails with
	// domain.ErrConflict.
	Create(ctx context.Context, k *domain.LoomingKey) error
	// ByID returns the key or domain.ErrNotFound.
	ByID(ctx context.Context, id string) (*domain.LoomingKey, error)
	// ListByPrincipal returns the principal's keys in stable creation
	// order (newest semantics owned by callers; oldest-first here).
	ListByPrincipal(ctx context.Context, principalID string) ([]*domain.LoomingKey, error)
	// ByHash resolves the validation digest (gateway validate path) or
	// domain.ErrNotFound.
	ByHash(ctx context.Context, hash [32]byte) (*domain.LoomingKey, error)
	// Revoke flips an active key to revoked exactly once: a one-way
	// UPDATE guarded by status='active'. Zero affected rows — missing
	// or already revoked — fails with domain.ErrAlreadyRevoked.
	Revoke(ctx context.Context, id string, now time.Time) error
	// CountSince counts keys the principal issued at or after since;
	// it feeds the issuance rate limit.
	CountSince(ctx context.Context, principalID string, since time.Time) (int, error)
}
