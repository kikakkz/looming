// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrInvalidInvite marks an invite token that cannot be consumed —
// unknown, expired, or already used. The registration endpoint maps it
// to a single 400 code; the wrapped detail stays server-side.
var ErrInvalidInvite = errors.New("identity: invalid invite token")

// ErrInviteEmailMismatch marks a registration presenting a bootstrap
// invite with an email other than the one the invite was minted for.
var ErrInviteEmailMismatch = errors.New("identity: invite email mismatch")

// ErrBootstrapClosed marks a bootstrap-invite mint outside the one-shot
// window: an admin principal exists, or a bootstrap invite was already
// created (topology-l1 §4).
var ErrBootstrapClosed = errors.New("identity: bootstrap window closed")

// Invite sources (migration 0003). Admin invites come from the
// authenticated admin API; bootstrap invites come from the one-shot
// first-admin endpoint and carry an email binding.
const (
	InviteSourceAdmin     = "admin"
	InviteSourceBootstrap = "bootstrap"
)

// InviteToken is an admin-generated, single-use registration voucher.
// The raw token is shown once at creation; only its SHA-256 hash is
// stored (credential hygiene, same rule as authn tokens). Source
// records which path minted the token; Email binds a bootstrap invite
// to its declared address (empty for admin invites).
type InviteToken struct {
	TokenHash []byte
	CreatedBy string
	Source    string
	Email     string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// GenerateInvite mints a new invite: 32 random bytes, base64url raw,
// SHA-256 hash at rest. rng and now are injected (AD-25: no wall-clock
// or global randomness in unit-testable paths).
func GenerateInvite(createdBy string, ttl time.Duration, rng io.Reader, now time.Time) (raw string, tok *InviteToken, err error) {
	if ttl <= 0 {
		return "", nil, fmt.Errorf("identity: invite ttl must be positive, got %s", ttl)
	}
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rng, buf); err != nil {
		return "", nil, fmt.Errorf("identity: invite randomness: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, &InviteToken{
		TokenHash: HashToken(raw),
		CreatedBy: createdBy,
		Source:    InviteSourceAdmin,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}, nil
}

// GenerateBootstrapInvite mints the one-shot first-admin voucher
// (topology-l1 §4): same construction as GenerateInvite, created_by
// tagged bootstrap and bound to the declared email. The window
// invariants are the caller's job (CheckBootstrapWindow) — persistence
// is out of the domain's reach.
func GenerateBootstrapInvite(email string, ttl time.Duration, rng io.Reader, now time.Time) (raw string, tok *InviteToken, err error) {
	raw, tok, err = GenerateInvite(InviteSourceBootstrap, ttl, rng, now)
	if err != nil {
		return "", nil, err
	}
	tok.Source = InviteSourceBootstrap
	tok.Email = email
	return raw, tok, nil
}

// CheckBootstrapWindow enforces the first-admin endpoint's one-shot
// window (topology-l1 §4): enabled only while no admin principal exists
// and no bootstrap invite has ever been created.
func CheckBootstrapWindow(hasAdmin, hasBootstrapInvite bool) error {
	if hasAdmin || hasBootstrapInvite {
		return fmt.Errorf("%w: admin principal exists=%v, bootstrap invite exists=%v",
			ErrBootstrapClosed, hasAdmin, hasBootstrapInvite)
	}
	return nil
}

// EnsureEmailMatch enforces a bootstrap invite's email binding: the
// registering principal must present the address the invite was minted
// for (case-insensitive, surrounding space ignored). Admin invites
// carry no binding and accept any email.
func (t *InviteToken) EnsureEmailMatch(email string) error {
	if t.Source != InviteSourceBootstrap {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(t.Email)) {
		return fmt.Errorf("%w: invite is bound to another email", ErrInviteEmailMismatch)
	}
	return nil
}

// Consume marks the invite used at the given time. Consuming twice, or
// consuming an expired invite, fails with ErrInvalidInvite.
func (t *InviteToken) Consume(now time.Time) error {
	if t.UsedAt != nil {
		return fmt.Errorf("%w: already consumed", ErrInvalidInvite)
	}
	if !now.Before(t.ExpiresAt) {
		return fmt.Errorf("%w: expired", ErrInvalidInvite)
	}
	used := now
	t.UsedAt = &used
	return nil
}

// HashToken derives the at-rest hash for a raw invite token. Exported
// for the app layer's invite lookup (the port works in hashes only).
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
