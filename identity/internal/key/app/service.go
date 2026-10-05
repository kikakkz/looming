// SPDX-License-Identifier: Apache-2.0

// Package app orchestrates the key capability's use cases: issuance
// (rate-limited, principal-gated), masked listing, repeatable reveal,
// and one-way revocation — owner-scoped on the self routes, scoped only
// by admin rights on the admin routes (identity-l1 §3 key journey).
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	keydomain "github.com/kikakkz/looming/identity/internal/key/domain"
	keyport "github.com/kikakkz/looming/identity/internal/key/port"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	principalport "github.com/kikakkz/looming/identity/internal/principal/port"
)

// Use-case errors, mapped to HTTP codes by the handlers.
var (
	// ErrPrincipalNotFound marks issuance (or an owner-scoped probe)
	// against a principal that does not exist. Owner-scoped reads map
	// cross-boundary probes here too: no existence leak.
	ErrPrincipalNotFound = errors.New("identity: principal not found")
	// ErrPrincipalInactive marks issuance for a pending or disabled
	// principal — only active principals may hold keys (fail closed).
	ErrPrincipalInactive = errors.New("identity: principal is not active")
	// ErrIssueLimit marks an issuance over the per-principal rate limit.
	ErrIssueLimit = errors.New("identity: key issuance rate limit reached")
)

// RevisionNotifier is the gateway-feed revision hub's consumer-side
// seam (the same hub instance reaches every mutating capability via
// dependency injection in cmd). Bumps after a successful write make
// the feed's watch return immediately; nil is tolerated so plain
// service use and tests stay light.
type RevisionNotifier interface {
	Bump()
}

// Service is the key use-case orchestrator.
type Service struct {
	repo        keyport.Repository
	principals  principalport.Repository
	sealer      keyport.Sealer
	notifier    RevisionNotifier
	rng         io.Reader
	clock       func() time.Time
	issueLimit  int
	issueWindow time.Duration
}

// NewService wires the service. clock and rng are injected (AD-25: no
// wall-clock or global randomness); rng must be a CSPRNG in production.
func NewService(repo keyport.Repository, principals principalport.Repository, sealer keyport.Sealer, rng io.Reader, clock func() time.Time, issueLimit int, issueWindow time.Duration) *Service {
	return &Service{
		repo:        repo,
		principals:  principals,
		sealer:      sealer,
		rng:         rng,
		clock:       clock,
		issueLimit:  issueLimit,
		issueWindow: issueWindow,
	}
}

// SetRevisionNotifier attaches the feed-revision hub; optional.
func (s *Service) SetRevisionNotifier(n RevisionNotifier) { s.notifier = n }

func (s *Service) bump() {
	if s.notifier != nil {
		s.notifier.Bump()
	}
}

// activePrincipal resolves the issuance gate target: existing and
// active, else the matching safe error.
func (s *Service) activePrincipal(ctx context.Context, principalID string) error {
	p, err := s.principals.ByID(ctx, principalID)
	if errors.Is(err, principaldomain.ErrNotFound) {
		return ErrPrincipalNotFound
	}
	if err != nil {
		return fmt.Errorf("identity: key principal lookup: %w", err)
	}
	if p.Status != principaldomain.StatusActive {
		return ErrPrincipalInactive
	}
	return nil
}

// Issue mints a key for the principal. The raw secret is returned
// exactly once here (and in reveal responses later); at rest only the
// SHA-256 hash and the sealed blob are stored. Rate limit: fewer than
// issueLimit keys within issueWindow.
func (s *Service) Issue(ctx context.Context, principalID, name string) (*keydomain.LoomingKey, keydomain.KeySecret, error) {
	if err := s.activePrincipal(ctx, principalID); err != nil {
		return nil, "", err
	}
	recent, err := s.repo.CountSince(ctx, principalID, s.clock().Add(-s.issueWindow))
	if err != nil {
		return nil, "", fmt.Errorf("identity: key issue count: %w", err)
	}
	if !keydomain.IssuanceAllowed(recent, s.issueLimit) {
		return nil, "", ErrIssueLimit
	}
	secret, err := keydomain.GenerateKeySecret(s.rng)
	if err != nil {
		return nil, "", err
	}
	sealed, err := s.sealer.Seal([]byte(secret))
	if err != nil {
		return nil, "", err
	}
	k, err := keydomain.NewLoomingKey(uuid.NewString(), principalID, name, secret, sealed, s.clock())
	if err != nil {
		return nil, "", err
	}
	if err := s.repo.Create(ctx, k); err != nil {
		return nil, "", err
	}
	s.bump()
	return k, secret, nil
}

// List returns the principal's keys; the aggregate shape is masked by
// construction — callers render display columns only.
func (s *Service) List(ctx context.Context, principalID string) ([]*keydomain.LoomingKey, error) {
	return s.repo.ListByPrincipal(ctx, principalID)
}

// Get returns one key owner-scoped; another principal's key maps to
// keydomain.ErrNotFound (no cross-boundary existence signal).
func (s *Service) Get(ctx context.Context, principalID, keyID string) (*keydomain.LoomingKey, error) {
	k, err := s.repo.ByID(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if k.PrincipalID != principalID {
		return nil, keydomain.ErrNotFound
	}
	return k, nil
}

// Reveal opens a key's sealed secret for its owner; the reveal is
// repeatable — the UI may offer a copy button on every view.
func (s *Service) Reveal(ctx context.Context, principalID, keyID string) (keydomain.KeySecret, error) {
	k, err := s.Get(ctx, principalID, keyID)
	if err != nil {
		return "", err
	}
	raw, err := s.sealer.Open(k.Sealed)
	if err != nil {
		return "", err
	}
	return keydomain.KeySecret(raw), nil
}

// AdminReveal is the admin-path reveal: same repeatable semantics, no
// owner scope.
func (s *Service) AdminReveal(ctx context.Context, keyID string) (keydomain.KeySecret, error) {
	k, err := s.repo.ByID(ctx, keyID)
	if err != nil {
		return "", err
	}
	raw, err := s.sealer.Open(k.Sealed)
	if err != nil {
		return "", err
	}
	return keydomain.KeySecret(raw), nil
}

// Revoke revokes an owner's key, one-way. A repeated (or raced) revoke
// surfaces keydomain.ErrAlreadyRevoked.
func (s *Service) Revoke(ctx context.Context, principalID, keyID string) error {
	if _, err := s.Get(ctx, principalID, keyID); err != nil {
		return err
	}
	if err := s.repo.Revoke(ctx, keyID, s.clock()); err != nil {
		return err
	}
	s.bump()
	return nil
}

// AdminRevoke revokes any key, one-way.
func (s *Service) AdminRevoke(ctx context.Context, keyID string) error {
	if err := s.repo.Revoke(ctx, keyID, s.clock()); err != nil {
		return err
	}
	s.bump()
	return nil
}

// AdminList lists a principal's keys for the admin console; identical
// data to List (masked shape), scoped by admin rights at the route.
func (s *Service) AdminList(ctx context.Context, principalID string) ([]*keydomain.LoomingKey, error) {
	return s.repo.ListByPrincipal(ctx, principalID)
}

// actorFrom reads the validated session the auth middleware stored; the
// self routes never run without it (cmd wiring), the zero value is a
// defensive fallback for direct mounting.
func actorFrom(ctx context.Context) string {
	info, ok := authnport.TokenInfoFrom(ctx)
	if !ok {
		return ""
	}
	return info.PrincipalID
}
