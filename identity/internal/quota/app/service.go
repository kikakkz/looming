// SPDX-License-Identifier: Apache-2.0

// Package app orchestrates the quota capability's use cases (identity-l1
// §6): admin set/get, the self view, and the engine-budget projection
// sweep. Propagation is best-effort by design — the quota row is the
// authority, engine budgets are its lagging projection.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	principalport "github.com/kikakkz/looming/identity/internal/principal/port"
	provisionport "github.com/kikakkz/looming/identity/internal/provision/port"
	quotadomain "github.com/kikakkz/looming/identity/internal/quota/domain"
	quotaport "github.com/kikakkz/looming/identity/internal/quota/port"
)

// ErrPrincipalNotFound marks a quota write against a principal that does
// not exist — the foreign-key guard turned into the v1 404 shape before
// the database has to speak.
var ErrPrincipalNotFound = errors.New("identity: principal not found")

// BudgetFailure is one engine credential whose budget update failed
// during quota propagation. The authority already changed; the
// projection lags visibly (identity-l1 §4).
type BudgetFailure struct {
	KeyID         string `json:"key_id"`
	Engine        string `json:"engine"`
	CredentialRef string `json:"credential_ref"`
	Message       string `json:"message"`
}

// Service is the quota use-case orchestrator.
type Service struct {
	repo        quotaport.Repository
	principals  principalport.Repository
	maps        provisionport.MapRepository
	provisioner provisionport.EngineProvisioner // nil = engine not configured
	clock       func() time.Time
}

// NewService wires the service. provisioner may be nil: without an
// engine there is nothing to project to. clock is injected (AD-25).
func NewService(repo quotaport.Repository, principals principalport.Repository, maps provisionport.MapRepository, provisioner provisionport.EngineProvisioner, clock func() time.Time) *Service {
	return &Service{
		repo:        repo,
		principals:  principals,
		maps:        maps,
		provisioner: provisioner,
		clock:       clock,
	}
}

// Set validates and stores the principal's quota, then sweeps the
// projection: every active engine credential mapped to the principal's
// keys receives SetBudget. Sweep failures are collected, never fatal —
// the response stays 200 and the lag is visible in
// budget_update_failures, including a failed sweep itself: the
// authority already changed, so the client must see the new quota
// (re-PUT is idempotent when the projection recovers).
func (s *Service) Set(ctx context.Context, principalID string, amount int64, unit string, windowDays int, actor string) (*quotadomain.Quota, []BudgetFailure, error) {
	q, err := quotadomain.NewQuota(principalID, amount, quotadomain.Unit(unit), windowDays, actor, s.clock())
	if err != nil {
		return nil, nil, err
	}
	if perr := s.principalExists(ctx, principalID); perr != nil {
		return nil, nil, perr
	}
	if uerr := s.repo.Upsert(ctx, q); uerr != nil {
		return nil, nil, fmt.Errorf("identity: quota upsert: %w", uerr)
	}
	failures, err := s.propagate(ctx, q)
	if err != nil {
		// The sweep itself failed (the map listing is identity-side, so
		// this is not an engine outage): the persisted authority still
		// returns 200 — the projection state is unknown and the failure
		// entry says so. The detail stays server-side (CWE-209).
		slog.ErrorContext(ctx, "identity: quota propagation sweep failed",
			"principal_id", q.PrincipalID, "err", err)
		return q, []BudgetFailure{{Message: "propagation sweep failed: engine budget projection state unknown"}}, nil
	}
	return q, failures, nil
}

// Get returns the principal's quota; the repository's ErrNoQuota maps
// northbound to 404 no_quota — the unlimited default is a first-class
// state, not an empty object.
func (s *Service) Get(ctx context.Context, principalID string) (*quotadomain.Quota, error) {
	return s.repo.ByPrincipal(ctx, principalID)
}

func (s *Service) principalExists(ctx context.Context, principalID string) error {
	_, err := s.principals.ByID(ctx, principalID)
	if errors.Is(err, principaldomain.ErrNotFound) {
		return ErrPrincipalNotFound
	}
	if err != nil {
		return fmt.Errorf("identity: quota principal lookup: %w", err)
	}
	return nil
}

func (s *Service) propagate(ctx context.Context, q *quotadomain.Quota) ([]BudgetFailure, error) {
	if s.provisioner == nil {
		return nil, nil
	}
	entries, _, err := s.maps.ListByPrincipal(ctx, q.PrincipalID, 0, 0)
	if err != nil {
		// The authority changed already; the caller reports the sweep
		// failure as a marker failure entry on the 200 (the detail stays
		// in this wrapped error, logged server-side).
		return nil, fmt.Errorf("identity: quota propagation sweep: %w", err)
	}
	failures := []BudgetFailure{}
	for _, entry := range entries {
		if err := s.provisioner.SetBudget(ctx, entry.CredentialRef, *q); err != nil {
			slog.ErrorContext(ctx, "quota budget projection failed",
				"principal_id", q.PrincipalID, "key_id", entry.KeyID,
				"engine", entry.Engine, "credential_ref", entry.CredentialRef, "err", err)
			failures = append(failures, BudgetFailure{
				KeyID:         entry.KeyID,
				Engine:        entry.Engine,
				CredentialRef: entry.CredentialRef,
				Message:       err.Error(),
			})
		}
	}
	return failures, nil
}
