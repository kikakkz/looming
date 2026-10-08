// SPDX-License-Identifier: Apache-2.0

// Package app orchestrates the topology capability's use cases:
// Declare, the converge-idempotent write of the deployment's desired
// state (topology-l1 §3 admin journey, AD-36 decision 2).
package app

import (
	"context"
	"errors"
	"time"

	domain "github.com/kikakkz/looming/platform/go/topologydomain"
	port "github.com/kikakkz/looming/platform/go/topologyport"
)

// Service is the topology use-case orchestrator.
type Service struct {
	store port.Store
	clock func() time.Time
}

// NewService wires the service. clock is injected (AD-25: no
// wall-clock in the use cases).
func NewService(store port.Store, clock func() time.Time) *Service {
	return &Service{store: store, clock: clock}
}

// DeclareInput is one desired-state declaration (the future
// `looming apply`'s payload — T1 parses the YAML into it).
type DeclareInput struct {
	// Hosts is the declared host-ID set: every placement must land on
	// one of these. Registration of the hosts themselves is the host
	// capability's job (T1 registers, then declares).
	Hosts []string
	// Placements is the desired component-placement set.
	Placements []domain.ComponentPlacement
	// Access is the desired access section (validated again here —
	// callers may construct it by hand).
	Access domain.Access
}

// Declare converges the stored topology toward the desired state. It
// validates the phase-1 invariant set (hosts ≥ 1, placements reference
// declared hosts, exactly one gateway front, no port conflicts, phase-1
// access vocabulary), then compares against the current state: an
// identical declaration is a no-op returning the current topology
// unchanged (converge-idempotency — no revision bump), a changed one
// persists the next revision under the optimistic-revision guard. A
// lost race fails with domain.ErrConflict; the caller re-reads and
// retries.
func (s *Service) Declare(ctx context.Context, in DeclareInput) (domain.Topology, error) {
	if len(in.Hosts) == 0 {
		return domain.Topology{}, domain.ErrNoHosts
	}
	if err := in.Access.Validate(); err != nil {
		return domain.Topology{}, err
	}
	if err := domain.ValidatePlacements(in.Hosts, in.Placements); err != nil {
		return domain.Topology{}, err
	}

	current, err := s.store.Current(ctx)
	if errors.Is(err, domain.ErrNoTopology) {
		current = domain.Uninitialized()
	} else if err != nil {
		return domain.Topology{}, err
	}

	if current.Matches(in.Access, in.Placements) {
		return current, nil
	}

	next := current.Next(in.Access, in.Placements, s.clock())
	if err := s.store.Save(ctx, next); err != nil {
		return domain.Topology{}, err
	}
	return next, nil
}
