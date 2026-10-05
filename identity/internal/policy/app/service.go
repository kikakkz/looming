// SPDX-License-Identifier: Apache-2.0

// Package app orchestrates the registration policy use cases: reading
// the active policy (with the admin-only default when the row is
// absent) and replacing it (identity-l1 §4).
package app

import (
	"context"
	"errors"
	"time"

	"github.com/kikakkz/looming/identity/internal/policy/domain"
	"github.com/kikakkz/looming/identity/internal/policy/port"
)

// Service is the policy use-case orchestrator.
type Service struct {
	store port.Store
	clock func() time.Time
}

// NewService wires the service; clock is injected (AD-25).
func NewService(store port.Store, clock func() time.Time) *Service {
	return &Service{store: store, clock: clock}
}

// Get returns the active policy. A missing row reads as the
// most restrictive default, admin-only: an empty table never opens
// registration by accident (fail closed).
func (s *Service) Get(ctx context.Context) (*domain.Policy, error) {
	p, err := s.store.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.New(domain.ModeAdminOnly, "system", s.clock())
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Set replaces the active policy, carrying the actor for the audit
// column (identity-l1 §4: policy change is audited).
func (s *Service) Set(ctx context.Context, mode domain.Mode, actor string) (*domain.Policy, error) {
	p, err := domain.New(mode, actor, s.clock())
	if err != nil {
		return nil, err
	}
	if err := s.store.Set(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}
