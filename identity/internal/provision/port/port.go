// SPDX-License-Identifier: Apache-2.0

// Package port defines the provisioning seams: the outbound
// EngineProvisioner (identity-l1 §5) and the IdentityMap persistence
// contract. Implementations: the LiteLLM admin-channel adapter and the
// postgres map repository.
package port

import (
	"context"

	"github.com/kikakkz/looming/identity/internal/provision/domain"
	quotadomain "github.com/kikakkz/looming/identity/internal/quota/domain"
)

// EngineProvisioner is the outbound port to the engine admin channel
// (identity-l1 §5): one engine credential per LoomingKey, budgets
// projected from the principal's Quota. Phase-1 implementation: LiteLLM
// (adapter/litellm). The credential VALUE leaves this seam exactly once,
// from Create; callers seal it for at-rest storage (the LoomingKey
// dual-track precedent) — identity persists only the reference.
type EngineProvisioner interface {
	// Create provisions a fresh engine credential. alias is the
	// caller-chosen stable identifier for the credential (the
	// idempotency anchor: re-creating after a rollback lands the same
	// alias). A nil quota provisions an unlimited credential — the
	// no-quota-row default. Returns the engine's reference (ref) and
	// the credential value (value).
	Create(ctx context.Context, alias string, quota *quotadomain.Quota) (ref, value string, err error)
	// SetBudget projects a quota change onto an existing credential
	// (admin set-quota propagation, identity-l1 §4).
	SetBudget(ctx context.Context, ref string, quota quotadomain.Quota) error
	// Delete removes a credential. Callers treat errors as best-effort:
	// revocation semantics live in the key capability.
	Delete(ctx context.Context, ref string) error
}

// MapRepository persists IdentityMap entries. The write side is split
// on purpose: creation rides the issuance transaction on the key
// repository (CreateWithProvision — both rows persist or neither), so
// this contract is the read/delete side plus the admin inspect path.
type MapRepository interface {
	// ListByKey returns every map entry of one LoomingKey.
	ListByKey(ctx context.Context, keyID string) ([]*domain.IdentityMap, error)
	// Delete removes the (key, engine) entry; a missing row is not an
	// error — revocation is one-way and best-effort at the engine, and
	// the row may already be gone from an earlier attempt.
	Delete(ctx context.Context, keyID, engine string) error
	// ListByPrincipal pages the principal's entries (resolved through
	// their keys), newest first; limit <= 0 disables the limit. total
	// counts across pages. It backs the admin inspect surface and the
	// quota propagation sweep.
	ListByPrincipal(ctx context.Context, principalID string, limit, offset int) ([]*domain.IdentityMap, int64, error)
}
