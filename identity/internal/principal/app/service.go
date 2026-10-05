// SPDX-License-Identifier: Apache-2.0

// Package app orchestrates the principal capability's use cases:
// policy-aware self-registration, admin provisioning, approval, and
// status administration (identity-l1 §3 onboarding journey).
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kikakkz/looming/identity/internal/policy/domain"
	policyport "github.com/kikakkz/looming/identity/internal/policy/port"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	"github.com/kikakkz/looming/identity/internal/principal/port"
)

// Use-case errors, mapped to HTTP codes by the handlers.
var (
	// ErrSelfRegistrationForbidden marks a self-registration attempt
	// while the active policy is admin-only.
	ErrSelfRegistrationForbidden = errors.New("identity: self-registration is disabled")
	// ErrPasswordTooShort marks a credential below MinPasswordLen.
	ErrPasswordTooShort = errors.New("identity: password too short")
	// ErrInvalidEmail marks a bootstrap-invite email that is empty or
	// not an address shape.
	ErrInvalidEmail = errors.New("identity: invalid email")
)

// MinPasswordLen is the credential floor at every registration input
// (self-registration and admin provisioning alike).
const MinPasswordLen = 12

// DefaultInviteTTL bounds admin-generated invites when the caller does
// not supply a ttl.
const DefaultInviteTTL = 24 * time.Hour

// PasswordHasher turns a plaintext credential into its at-rest encoding
// (argon2id). Defined consumer-side: the authn adapter's local
// implementation is wired in at composition.
type PasswordHasher interface {
	Hash(password string) (string, error)
}

// Service is the principal use-case orchestrator.
type Service struct {
	repo     port.Repository
	invites  port.InviteRepository
	policy   policyport.Store
	hasher   PasswordHasher
	notifier RevisionNotifier
	rng      io.Reader
	clock    func() time.Time
}

// RevisionNotifier is the gateway-feed revision hub's consumer-side
// seam; cmd injects the shared hub. Status changes move keys in and
// out of the feed (a disabled principal's keys vanish), so the feed
// must wake on every persisted transition. Nil is tolerated.
type RevisionNotifier interface {
	Bump()
}

// SetRevisionNotifier attaches the feed-revision hub; optional.
func (s *Service) SetRevisionNotifier(n RevisionNotifier) { s.notifier = n }

func (s *Service) bump() {
	if s.notifier != nil {
		s.notifier.Bump()
	}
}

// NewService wires the service. clock and rng are injected (AD-25: no
// wall-clock or global randomness); rng must be a CSPRNG in production.
func NewService(repo port.Repository, invites port.InviteRepository, policy policyport.Store, hasher PasswordHasher, rng io.Reader, clock func() time.Time) *Service {
	return &Service{repo: repo, invites: invites, policy: policy, hasher: hasher, rng: rng, clock: clock}
}

// RegisterInput is a self-registration request.
type RegisterInput struct {
	Username    string
	Password    string
	DisplayName string
	// InviteToken is required when the active policy mode is invite.
	// A bootstrap-sourced token instead bypasses the policy entirely
	// (the first admin registers even under admin-only).
	InviteToken string
	// Email is matched against a bootstrap invite's binding; ignored
	// for every other registration path.
	Email string
}

// Register creates a human principal according to the active
// registration policy. The policy read fails closed: a registration
// attempt with an unreadable policy is denied, never defaulted.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*principaldomain.Principal, error) {
	pol, err := s.policy.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity: registration policy unavailable, denying registration: %w", err)
	}
	if len(in.Password) < MinPasswordLen {
		return nil, fmt.Errorf("%w: need at least %d chars", ErrPasswordTooShort, MinPasswordLen)
	}
	if in.InviteToken != "" {
		p, bootstrap, probeErr := s.registerWithBootstrapInvite(ctx, in)
		if bootstrap || probeErr != nil {
			return p, probeErr
		}
		// Not a bootstrap voucher: the policy gate below decides.
	}
	initial, err := initialStatusForPolicy(pol.Mode, in.InviteToken != "")
	if err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, err
	}
	// All input validation happens before the invite is consumed: a
	// rejected input must not burn a voucher.
	p, err := principaldomain.NewRegistration(
		uuid.NewString(),
		normalizeUsername(in.Username),
		principaldomain.KindHuman,
		in.DisplayName,
		hash,
		initial,
		s.clock(),
	)
	if err != nil {
		return nil, err
	}
	if pol.Mode == domain.ModeInvite {
		if err := s.registerWithInvite(ctx, p, in.InviteToken); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// initialStatusForPolicy maps the active registration-policy mode to
// the new principal's initial status, rejecting registrations the mode
// forbids (identity-l1 §3).
func initialStatusForPolicy(mode domain.Mode, hasInvite bool) (principaldomain.Status, error) {
	switch mode {
	case domain.ModeAdminOnly:
		return "", ErrSelfRegistrationForbidden
	case domain.ModeInvite:
		if !hasInvite {
			return "", fmt.Errorf("%w: invite token required", principaldomain.ErrInvalidInvite)
		}
		return principaldomain.StatusActive, nil
	case domain.ModeSelfRegisterWithApproval:
		return principaldomain.StatusPending, nil
	default:
		return "", fmt.Errorf("%w: active policy mode %q", domain.ErrInvalidMode, mode)
	}
}

// registerWithBootstrapInvite handles a registration that presented an
// invite token: when the voucher is bootstrap-sourced it bypasses the
// registration-policy mode entirely and lands the first admin (active,
// roles admin+member — topology-l1 §4). ok is false when the token is
// not a bootstrap voucher, letting the caller fall through to the
// policy gate; a lookup failure that is not "not found" denies
// registration (fail closed, without revealing the token's nature).
func (s *Service) registerWithBootstrapInvite(ctx context.Context, in RegisterInput) (p *principaldomain.Principal, ok bool, err error) {
	tok, err := s.invites.ByHash(ctx, principaldomain.HashToken(in.InviteToken))
	if err != nil {
		if errors.Is(err, principaldomain.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("identity: invite lookup failed, denying registration: %w", err)
	}
	if tok.Source != principaldomain.InviteSourceBootstrap {
		return nil, false, nil
	}
	if consumeErr := tok.Consume(s.clock()); consumeErr != nil {
		return nil, true, consumeErr
	}
	if matchErr := tok.EnsureEmailMatch(in.Email); matchErr != nil {
		return nil, true, matchErr
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, true, err
	}
	candidate, err := principaldomain.NewRegistration(
		uuid.NewString(),
		normalizeUsername(in.Username),
		principaldomain.KindHuman,
		in.DisplayName,
		hash,
		principaldomain.StatusActive,
		s.clock(),
		principaldomain.WithRoles([]string{principaldomain.RoleAdmin, principaldomain.RoleMember}),
	)
	if err != nil {
		return nil, true, err
	}
	if _, err := s.repo.ByUsername(ctx, candidate.Username); err == nil {
		return nil, true, principaldomain.ErrUsernameTaken
	} else if !errors.Is(err, principaldomain.ErrNotFound) {
		return nil, true, err
	}
	// Consume and create in one transaction: a failed insert must not
	// burn the one-shot voucher (the bootstrap window can never
	// re-open). A lost consume race surfaces as ErrInvalidInvite.
	if err := s.repo.CreateWithInviteConsume(ctx, candidate, tok.TokenHash, *tok.UsedAt); err != nil {
		if errors.Is(err, principaldomain.ErrConflict) {
			return nil, true, fmt.Errorf("%w: invite already consumed", principaldomain.ErrInvalidInvite)
		}
		return nil, true, err
	}
	return candidate, true, nil
}

// registerWithInvite runs the invite-mode gate for a validated
// registration candidate: validate the voucher first (an invalid invite
// must not reveal whether a username is taken — enumeration guard),
// then reject a taken username without burning the voucher, then
// consume exactly once. The narrow race left behind (a concurrent
// create between the check and the consume) is documented: the voucher
// is single-use, so a lost race burns one invite without a principal —
// the safe direction.
func (s *Service) registerWithInvite(ctx context.Context, p *principaldomain.Principal, raw string) error {
	tok, err := s.inspectInvite(ctx, raw)
	if err != nil {
		return err
	}
	if _, err := s.repo.ByUsername(ctx, p.Username); err == nil {
		return principaldomain.ErrUsernameTaken
	} else if !errors.Is(err, principaldomain.ErrNotFound) {
		return err
	}
	if err := s.invites.MarkUsed(ctx, tok.TokenHash, *tok.UsedAt); err != nil {
		// A raced consume loses the guard: the voucher is gone.
		return fmt.Errorf("%w: invite already consumed", principaldomain.ErrInvalidInvite)
	}
	return nil
}

// inspectInvite loads the invite and checks it is consumable without
// consuming it. The caller marks it used only after every other
// validation has passed, so a rejected registration never burns the
// voucher.
func (s *Service) inspectInvite(ctx context.Context, raw string) (*principaldomain.InviteToken, error) {
	tok, err := s.invites.ByHash(ctx, principaldomain.HashToken(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: unknown invite token", principaldomain.ErrInvalidInvite)
	}
	if err := tok.Consume(s.clock()); err != nil {
		return nil, err
	}
	return tok, nil
}

// ProvisionInput is an admin provisioning request. Any policy mode
// allows provisioning; provisioned principals start active
// (identity-l1 §3).
type ProvisionInput struct {
	Username    string
	Password    string
	DisplayName string
	Kind        principaldomain.Kind
	// Roles overrides the default member role when non-empty.
	Roles []string
}

// Provision creates a principal outside the self-registration policy.
func (s *Service) Provision(ctx context.Context, in ProvisionInput) (*principaldomain.Principal, error) {
	if len(in.Password) < MinPasswordLen {
		return nil, fmt.Errorf("%w: need at least %d chars", ErrPasswordTooShort, MinPasswordLen)
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, err
	}
	opts := []principaldomain.RegistrationOption{}
	if len(in.Roles) > 0 {
		opts = append(opts, principaldomain.WithRoles(in.Roles))
	}
	p, err := principaldomain.NewRegistration(
		uuid.NewString(),
		normalizeUsername(in.Username),
		in.Kind,
		in.DisplayName,
		hash,
		principaldomain.StatusActive,
		s.clock(),
		opts...,
	)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Approve moves a pending principal to active (identity:approve).
func (s *Service) Approve(ctx context.Context, id string) (*principaldomain.Principal, error) {
	p, err := s.repo.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := p.Approve(s.clock()); err != nil {
		return nil, err
	}
	approved, updateErr := s.repo.UpdateStatus(ctx, p)
	if updateErr != nil {
		return nil, updateErr
	}
	s.bump()
	return approved, nil
}

// SetStatus disables or re-enables a principal, enforcing the domain
// transition rules.
func (s *Service) SetStatus(ctx context.Context, id string, status principaldomain.Status) (*principaldomain.Principal, error) {
	p, err := s.repo.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := p.SetStatus(status, s.clock()); err != nil {
		return nil, err
	}
	updated, updateErr := s.repo.UpdateStatus(ctx, p)
	if updateErr != nil {
		return nil, updateErr
	}
	s.bump()
	return updated, nil
}

// Get returns one principal.
func (s *Service) Get(ctx context.Context, id string) (*principaldomain.Principal, error) {
	return s.repo.ByID(ctx, id)
}

// List returns a page of principals plus the total count.
func (s *Service) List(ctx context.Context, limit, offset int) ([]*principaldomain.Principal, int64, error) {
	return s.repo.List(ctx, limit, offset)
}

// CreateInvite mints an admin invite voucher. The raw token is returned
// exactly once; only its hash is stored.
func (s *Service) CreateInvite(ctx context.Context, createdBy string, ttl time.Duration) (raw string, tok *principaldomain.InviteToken, err error) {
	if ttl <= 0 {
		ttl = DefaultInviteTTL
	}
	raw, tok, err = principaldomain.GenerateInvite(createdBy, ttl, s.rng, s.clock())
	if err != nil {
		return "", nil, err
	}
	if err := s.invites.Create(ctx, tok); err != nil {
		return "", nil, err
	}
	return raw, tok, nil
}

// CreateBootstrapInvite mints the one-shot first-admin voucher
// (topology-l1 §4) for the deployment's declared initial admin email.
// The window invariants are domain rules (CheckBootstrapWindow), and
// the insert itself is backstopped by the database: a concurrent mint
// that passes the read-only check loses the unique index and maps to
// ErrBootstrapClosed (409), not a 500.
func (s *Service) CreateBootstrapInvite(ctx context.Context, email string, ttl time.Duration) (raw string, tok *principaldomain.InviteToken, err error) {
	email = normalizeEmail(email)
	if !validEmail(email) {
		return "", nil, fmt.Errorf("%w: %q", ErrInvalidEmail, email)
	}
	hasAdmin, err := s.repo.ExistsAdmin(ctx)
	if err != nil {
		return "", nil, err
	}
	hasBootstrap, err := s.invites.ExistsBySource(ctx, principaldomain.InviteSourceBootstrap)
	if err != nil {
		return "", nil, err
	}
	if windowErr := principaldomain.CheckBootstrapWindow(hasAdmin, hasBootstrap); windowErr != nil {
		return "", nil, windowErr
	}
	if ttl <= 0 {
		ttl = DefaultInviteTTL
	}
	raw, tok, err = principaldomain.GenerateBootstrapInvite(email, ttl, s.rng, s.clock())
	if err != nil {
		return "", nil, err
	}
	if err := s.invites.Create(ctx, tok); err != nil {
		if errors.Is(err, principaldomain.ErrConflict) {
			return "", nil, fmt.Errorf("%w: a concurrent mint won the one-shot race", principaldomain.ErrBootstrapClosed)
		}
		return "", nil, err
	}
	return raw, tok, nil
}

// normalizeUsername lowercases before validation and storage: the shape
// rule is lowercase-only, and uniqueness must not depend on case.
func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// normalizeEmail applies the same case-and-space policy to the bootstrap
// binding address; the domain's EnsureEmailMatch compares
// case-insensitively, so this protects the stored shape, not the match.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validEmail is a deliberately shallow sanity check — the operator
// carries the voucher to the mailbox, so deliverability is their
// review; the endpoint only rejects shapes that cannot be an address.
func validEmail(email string) bool {
	return strings.Contains(email, "@")
}
