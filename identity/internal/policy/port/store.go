// SPDX-License-Identifier: Apache-2.0

// Package port defines the registration policy persistence seam.
package port

import (
	"context"

	"github.com/kikakkz/looming/identity/internal/policy/domain"
)

// Store persists the policy singleton. Set upserts the single row:
// exactly-one-active holds structurally by fixed PK (identity-l1 §4).
type Store interface {
	// Get returns the active policy or domain.ErrNotFound.
	Get(ctx context.Context) (*domain.Policy, error)
	// Set overwrites the singleton and refreshes its audit columns.
	Set(ctx context.Context, p *domain.Policy) error
}
