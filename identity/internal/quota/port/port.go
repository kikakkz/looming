// SPDX-License-Identifier: Apache-2.0

// Package port defines the quota capability's persistence contract.
// The postgres adapter implements it; the app layer stays
// storage-agnostic.
package port

import (
	"context"

	"github.com/kikakkz/looming/identity/internal/quota/domain"
)

// Repository persists Quotas: one row per principal, the row's absence
// is the unlimited default. Error contract: domain.ErrNoQuota from
// ByPrincipal when the principal carries no quota row.
type Repository interface {
	// Upsert inserts or replaces the principal's quota.
	Upsert(ctx context.Context, q *domain.Quota) error
	// ByPrincipal returns the quota or domain.ErrNoQuota.
	ByPrincipal(ctx context.Context, principalID string) (*domain.Quota, error)
}
