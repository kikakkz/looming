// SPDX-License-Identifier: Apache-2.0

// Package app is the host capability's use-case layer. T0's service is
// a deliberate pass-through over the Registry port: registration and
// re-join orchestration belong to T2's join flow, so the app owns no
// policy yet — it only pins the error contract the transport layers
// will rely on.
package app

import (
	"context"

	domain "github.com/kikakkz/looming/platform/go/hostdomain"
	port "github.com/kikakkz/looming/platform/go/hostport"
)

// Service delegates host registration primitives to the Registry port.
type Service struct {
	registry port.Registry
}

// NewService wires the service.
func NewService(registry port.Registry) *Service {
	return &Service{registry: registry}
}

// Register delegates to the registry's address-keyed upsert.
func (s *Service) Register(ctx context.Context, h *domain.Host) (*domain.Host, error) {
	return s.registry.Register(ctx, h)
}

// ByID delegates to the registry.
func (s *Service) ByID(ctx context.Context, id string) (*domain.Host, error) {
	return s.registry.ByID(ctx, id)
}

// ByAddress delegates to the registry.
func (s *Service) ByAddress(ctx context.Context, address string) (*domain.Host, error) {
	return s.registry.ByAddress(ctx, address)
}

// List delegates the observed-facts read to the registry (slice 1.3's
// admin-side pull — GET /v1/internal/hosts).
func (s *Service) List(ctx context.Context) ([]domain.Host, error) {
	return s.registry.List(ctx)
}

// Update delegates the re-join address/label refresh to the registry.
func (s *Service) Update(ctx context.Context, h *domain.Host) (*domain.Host, error) {
	return s.registry.Update(ctx, h)
}
