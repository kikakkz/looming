// SPDX-License-Identifier: Apache-2.0

// Package domain holds the principal aggregate and its value objects:
// the identity primitive, its status machine, and the invite token
// (hash-at-rest, one-way consume; admin- or bootstrap-minted).
// Pure model — stdlib only (AD-23/AD-24).
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Kind classifies a principal. Service principals represent agent
// workloads; human principals represent users (identity-l1 §2).
type Kind string

const (
	KindHuman   Kind = "human"
	KindService Kind = "service"
)

// Status is the principal lifecycle state. pending → active → disabled,
// with disabled → active re-enable; pending may also go straight to
// disabled (identity-l1 §4).
type Status string

const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// Builtin roles (identity-l1 §2). Custom roles are a deferred slice.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Domain errors. Persistence-facing sentinels (ErrNotFound,
// ErrUsernameTaken, ErrConflict) are defined here because uniqueness and
// optimistic concurrency are aggregate invariants, not adapter details.
var (
	ErrInvalidKind        = errors.New("identity: invalid principal kind")
	ErrInvalidStatus      = errors.New("identity: invalid principal status")
	ErrInvalidTransition  = errors.New("identity: invalid principal status transition")
	ErrInvalidUsername    = errors.New("identity: invalid username")
	ErrDisplayNameTooLong = errors.New("identity: display name too long")
	ErrNotFound           = errors.New("identity: principal not found")
	ErrUsernameTaken      = errors.New("identity: username already taken")
	ErrConflict           = errors.New("identity: conflicting write")
	// ErrLastAdmin marks a status change that would disable the sole
	// active admin — the deployment would lock itself out (no active
	// admin could ever approve, provision, or re-enable).
	ErrLastAdmin = errors.New("identity: cannot disable the last active admin")
)

// Username bounds and shape: 3-64 chars, lowercase alphanumeric with
// ._- separators, no leading/trailing separator.
const (
	UsernameMinLen    = 3
	UsernameMaxLen    = 64
	DisplayNameMaxLen = 128
)

var usernameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// ValidUsername reports whether username satisfies the shape rule.
func ValidUsername(username string) bool {
	return len(username) >= UsernameMinLen &&
		len(username) <= UsernameMaxLen &&
		usernameRe.MatchString(username)
}

// Principal is the identity aggregate root. PasswordHash is an argon2id
// encoding; empty means no local credential (e.g. an OIDC-bound
// principal in a later slice).
type Principal struct {
	ID           string
	Username     string
	Kind         Kind
	DisplayName  string
	PasswordHash string
	Status       Status
	Roles        []string
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RegistrationOption tunes NewRegistration.
type RegistrationOption func(*Principal)

// WithRoles overrides the default member role. The slice is copied.
func WithRoles(roles []string) RegistrationOption {
	return func(p *Principal) {
		p.Roles = append([]string(nil), roles...)
	}
}

// NewRegistration builds a principal at registration time. The initial
// status is the caller's policy decision (identity-l1 §3): admin
// provision and invite-token registration start active, plain
// self-registration starts pending awaiting approval.
func NewRegistration(id, username string, kind Kind, displayName, passwordHash string, status Status, now time.Time, opts ...RegistrationOption) (*Principal, error) {
	if !ValidUsername(username) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidUsername, username)
	}
	switch kind {
	case KindHuman, KindService:
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidKind, kind)
	}
	switch status {
	case StatusPending, StatusActive:
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidStatus, status)
	}
	if len(displayName) > DisplayNameMaxLen {
		return nil, fmt.Errorf("%w: %d chars", ErrDisplayNameTooLong, len(displayName))
	}
	p := &Principal{
		ID:           id,
		Username:     username,
		Kind:         kind,
		DisplayName:  displayName,
		PasswordHash: passwordHash,
		Status:       status,
		Roles:        []string{RoleMember},
		Version:      1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// Approve moves a pending principal to active (identity:approve).
func (p *Principal) Approve(now time.Time) error {
	return p.transition(StatusPending, StatusActive, now)
}

// Activate re-enables a disabled principal.
func (p *Principal) Activate(now time.Time) error {
	return p.transition(StatusDisabled, StatusActive, now)
}

// Disable deactivates a pending or active principal.
func (p *Principal) Disable(now time.Time) error {
	if p.Status != StatusPending && p.Status != StatusActive {
		return fmt.Errorf("%w: cannot disable %s principal", ErrInvalidTransition, p.Status)
	}
	p.Status = StatusDisabled
	p.UpdatedAt = now
	return nil
}

// SetStatus dispatches a target status to its transition method; it is
// the admin status endpoint's domain entry point.
func (p *Principal) SetStatus(status Status, now time.Time) error {
	switch status {
	case StatusDisabled:
		return p.Disable(now)
	case StatusActive:
		return p.Activate(now)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidStatus, status)
	}
}

func (p *Principal) transition(from, to Status, now time.Time) error {
	if p.Status != from {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, p.Status, to)
	}
	p.Status = to
	p.UpdatedAt = now
	return nil
}

// HasRole reports whether the principal carries role.
func (p *Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}
