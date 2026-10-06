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
	"log/slog"
	"time"

	"github.com/google/uuid"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	keydomain "github.com/kikakkz/looming/identity/internal/key/domain"
	keyport "github.com/kikakkz/looming/identity/internal/key/port"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	principalport "github.com/kikakkz/looming/identity/internal/principal/port"
	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
	provisionport "github.com/kikakkz/looming/identity/internal/provision/port"
	quotadomain "github.com/kikakkz/looming/identity/internal/quota/domain"
	quotaport "github.com/kikakkz/looming/identity/internal/quota/port"
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
	// ErrProvisionFailed marks an issuance whose engine-credential step
	// failed. Nothing is persisted when it surfaces; the handler maps it
	// to 502 provision_failed (identity-l1 §6).
	ErrProvisionFailed = errors.New("identity: engine credential provisioning failed")
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
	// Engine provisioning (identity-l1 §3 journey 2). All nil/empty when
	// no engine is configured (IDENTITY_ENGINE_URL unset): issuance then
	// works without provisioning and the map stays empty — the gateway
	// falls back per the feed contract.
	provisioner provisionport.EngineProvisioner
	maps        provisionport.MapRepository
	quotas      quotaport.Repository
	engineName  string
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

// SetEngineProvisioner attaches the optional provision step to issuance
// (identity-l1 §3 journey 2). Called by cmd only when an engine is
// configured; until then every field stays nil and Issue persists
// key-only.
func (s *Service) SetEngineProvisioner(p provisionport.EngineProvisioner, maps provisionport.MapRepository, quotas quotaport.Repository, engineName string) {
	s.provisioner = p
	s.maps = maps
	s.quotas = quotas
	s.engineName = engineName
}

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
	if s.provisioner != nil {
		if err := s.issueProvisioned(ctx, k); err != nil {
			return nil, "", err
		}
	} else if err := s.repo.Create(ctx, k); err != nil {
		return nil, "", err
	}
	s.bump()
	return k, secret, nil
}

// issueProvisioned persists a key together with its engine credential.
// Order matters (identity-l1 §4): the engine credential is created
// FIRST, then the identity rows in one transaction; an identity-write
// failure deletes the engine credential immediately — same-operation
// rollback, not a compensation layer (AD-30) — and nothing is
// persisted. A provisioning failure surfaces ErrProvisionFailed and
// likewise persists nothing.
func (s *Service) issueProvisioned(ctx context.Context, k *keydomain.LoomingKey) error {
	quota, err := s.quotaFor(ctx, k.PrincipalID)
	if err != nil {
		return err
	}
	// The alias anchors idempotency (identity-l1 §7): a rollback and
	// re-issue of the same key id lands the same engine-side name.
	alias := "looming-" + k.ID
	ref, value, err := s.provisioner.Create(ctx, alias, quota)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProvisionFailed, err)
	}
	sealed, err := s.sealer.Seal([]byte(value))
	if err != nil {
		s.rollbackProvisioned(ctx, ref)
		return err
	}
	entry, err := provisiondomain.NewIdentityMap(k.ID, s.engineName, ref, sealed, s.clock())
	if err != nil {
		s.rollbackProvisioned(ctx, ref)
		return err
	}
	if err := s.repo.CreateWithProvision(ctx, k, entry); err != nil {
		s.rollbackProvisioned(ctx, ref)
		return err
	}
	return nil
}

// rollbackProvisioned deletes a credential whose identity write failed.
// If the delete fails too (engine now unreachable), the credential is
// orphaned: it is inert — nothing references it and the budget it
// carries can never be spent — and the error is logged for an operator.
func (s *Service) rollbackProvisioned(ctx context.Context, ref string) {
	if err := s.provisioner.Delete(ctx, ref); err != nil {
		slog.ErrorContext(ctx, "identity: engine credential rollback delete failed; orphan credential is inert",
			"credential_ref", ref, "err", err)
	}
}

// quotaFor resolves the principal's quota for the new credential. The
// no-quota-row default is an unlimited credential: nil, nil.
func (s *Service) quotaFor(ctx context.Context, principalID string) (*quotadomain.Quota, error) {
	q, err := s.quotas.ByPrincipal(ctx, principalID)
	if errors.Is(err, quotadomain.ErrNoQuota) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity: provision quota lookup: %w", err)
	}
	return q, nil
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
	s.revokeProvision(ctx, keyID)
	return nil
}

// AdminRevoke revokes any key, one-way.
func (s *Service) AdminRevoke(ctx context.Context, keyID string) error {
	if err := s.repo.Revoke(ctx, keyID, s.clock()); err != nil {
		return err
	}
	s.bump()
	s.revokeProvision(ctx, keyID)
	return nil
}

// revokeProvision propagates a revocation to the key's engine
// credentials (identity-l1 §3 journey 2: the key is the unit of
// revocation). The map entry dies FIRST so the feed stops serving the
// credential immediately; the engine delete is best-effort and failure
// never blocks revocation — the map row is gone either way, so nothing
// references the credential, and an orphaned engine credential is inert.
// Failures are logged for an operator.
func (s *Service) revokeProvision(ctx context.Context, keyID string) {
	if s.provisioner == nil || s.maps == nil {
		return
	}
	entries, err := s.maps.ListByKey(ctx, keyID)
	if err != nil {
		slog.ErrorContext(ctx, "identity: provision map list failed at revoke", "key_id", keyID, "err", err)
		return
	}
	for _, entry := range entries {
		if err := s.maps.Delete(ctx, entry.KeyID, entry.Engine); err != nil {
			slog.ErrorContext(ctx, "identity: provision map delete failed at revoke",
				"key_id", entry.KeyID, "engine", entry.Engine, "err", err)
		}
		if err := s.provisioner.Delete(ctx, entry.CredentialRef); err != nil {
			slog.ErrorContext(ctx, "identity: engine credential delete failed at revoke; orphan credential is inert",
				"key_id", entry.KeyID, "engine", entry.Engine, "credential_ref", entry.CredentialRef, "err", err)
		}
	}
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
