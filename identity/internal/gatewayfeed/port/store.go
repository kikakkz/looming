// SPDX-License-Identifier: Apache-2.0

// Package port defines the gateway feed's driven seam: the read model
// the feed and validate endpoints project from. The postgres adapter
// implements it; the app layer stays storage-agnostic.
package port

import (
	"context"

	"github.com/kikakkz/looming/identity/internal/gatewayfeed/domain"
)

// Store is the feed's private read model (the authn-adapter precedent:
// adapters may read another capability's table via their own SQL when
// the shape is a projection, not the aggregate).
type Store interface {
	// ListKeys returns the keys of ACTIVE principals only — a disabled
	// principal's keys are omitted outright (fail closed). Revoked keys
	// are included with their status so syncers can distinguish delete
	// from never-present. Active keys carry their EngineCredential when
	// provisioned (empty otherwise).
	ListKeys(ctx context.Context) ([]domain.Key, error)
	// ListPrincipals returns every principal and its status.
	ListPrincipals(ctx context.Context) ([]domain.Principal, error)
	// ByHash resolves the validation digest joined with its principal's
	// status, or domain.ErrNotFound. Revoked keys and non-active
	// principals resolve here with their real statuses; the service
	// owns the fail-closed policy and maps both to ErrNotFound (one
	// 404 shape — no existence signal).
	ByHash(ctx context.Context, hash []byte) (domain.Key, string, error)
}

// CredentialOpener unseals the engine credentials the feed projects.
// It is the key capability's Sealer satisfied structurally in cmd —
// defined here so the feed's seam stays feed-local.
type CredentialOpener interface {
	Open(sealed []byte) ([]byte, error)
}
