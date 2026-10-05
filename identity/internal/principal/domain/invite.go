// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrInvalidInvite marks an invite token that cannot be consumed —
// unknown, expired, or already used. The registration endpoint maps it
// to a single 400 code; the wrapped detail stays server-side.
var ErrInvalidInvite = errors.New("identity: invalid invite token")

// InviteToken is an admin-generated, single-use registration voucher.
// The raw token is shown once at creation; only its SHA-256 hash is
// stored (credential hygiene, same rule as authn tokens).
type InviteToken struct {
	TokenHash []byte
	CreatedBy string
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
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}, nil
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
