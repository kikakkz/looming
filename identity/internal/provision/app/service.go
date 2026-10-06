// SPDX-License-Identifier: Apache-2.0

// Package app orchestrates the IdentityMap's northbound read side
// (identity-l1 §6): the admin inspect surface. Writes ride other
// capabilities' transactions — creation rides key issuance
// (CreateWithProvision), deletion rides revocation — so this service
// owns reads only.
package app

import (
	"context"

	"github.com/kikakkz/looming/identity/internal/provision/domain"
	"github.com/kikakkz/looming/identity/internal/provision/port"
)

// Service is the IdentityMap inspect orchestrator.
type Service struct {
	maps port.MapRepository
}

// NewService wires the service.
func NewService(maps port.MapRepository) *Service {
	return &Service{maps: maps}
}

// Inspect pages one principal's map entries (resolved through their
// keys). limit <= 0 disables the limit; total counts across pages.
func (s *Service) Inspect(ctx context.Context, principalID string, limit, offset int) ([]*domain.IdentityMap, int64, error) {
	return s.maps.ListByPrincipal(ctx, principalID, limit, offset)
}
