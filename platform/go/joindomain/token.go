// SPDX-License-Identifier: Apache-2.0

// Package domain holds the JoinToken aggregate (topology-l1 §5): the
// one-time, TTL'd, hash-stored, role-scoped voucher a new host presents
// to register itself — the kubeadm shape. Pure model — stdlib only
// (AD-23/AD-24).
package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

// Persistence- and use-case-facing sentinels. The distinct consume
// failures matter: expired vs used map to different HTTP codes at the
// handler, so they cannot share a sentinel.
var (
	// ErrInvalidRole marks a mint outside the phase-1 role vocabulary.
	ErrInvalidRole = errors.New("topology: invalid join-token role")
	// ErrTokenNotFound marks a consume for a hash no row holds.
	ErrTokenNotFound = errors.New("topology: join token not found")
	// ErrTokenExpired marks a consume at or past the token's expiry.
	ErrTokenExpired = errors.New("topology: join token expired")
	// ErrTokenUsed marks a consume of an already-consumed (or revoked)
	// token; the store's guarded UPDATE also surfaces it for a raced
	// consume, making consumption exactly-once under concurrency.
	ErrTokenUsed = errors.New("topology: join token already used")
)

// Phase-1 join roles (topology-l1 §7). Role enforcement differences are
// deferred; the role is recorded on the joining host's labels.
const (
	RoleEngine = "engine"
	RoleWorker = "worker"
)

const (
	// TokenByteLen is the raw random material behind one join token.
	TokenByteLen = 32
	// PrefixByteLen is how many hash bytes the list/revoke vocabulary
	// shows: enough to eyeball a token, short enough to paste.
	PrefixByteLen = 6
)

// JoinToken is the one-time registration voucher. The raw token exists
// only at mint time (printed once); at rest only its SHA-256 hash.
type JoinToken struct {
	TokenHash []byte
	Role      string
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// ValidRole reports whether role is in the phase-1 vocabulary.
func ValidRole(role string) bool {
	switch role {
	case RoleEngine, RoleWorker:
		return true
	}
	return false
}

// GenerateToken mints a join token: 32 random bytes, base64url raw for
// transport, SHA-256 hash for storage. rng and now are injected (AD-25:
// no wall-clock or global randomness in unit-testable paths). A bad role
// or non-positive ttl fails before any randomness is drawn, so nothing
// usable leaks from a rejected mint.
func GenerateToken(role, createdBy string, ttl time.Duration, rng io.Reader, now time.Time) (raw string, tok *JoinToken, err error) {
	if !ValidRole(role) {
		return "", nil, fmt.Errorf("%w: %q (phase-1: %s, %s)", ErrInvalidRole, role, RoleEngine, RoleWorker)
	}
	if ttl <= 0 {
		return "", nil, fmt.Errorf("topology: join-token ttl must be positive, got %s", ttl)
	}
	buf := make([]byte, TokenByteLen)
	if _, err := io.ReadFull(rng, buf); err != nil {
		return "", nil, fmt.Errorf("topology: join-token randomness: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, &JoinToken{
		TokenHash: HashToken(raw),
		Role:      role,
		CreatedBy: createdBy,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}, nil
}

// HashToken derives the at-rest hash for a raw join token. Exported for
// the app layer's consume lookup (the port works in hashes only).
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// HashTokenHex renders the full hash in hex — the admin-side revoke
// vocabulary accepts a prefix of it.
func HashTokenHex(raw string) string {
	return hex.EncodeToString(HashToken(raw))
}

// Prefix is the list/revoke vocabulary slice of the stored hash: the
// first PrefixByteLen bytes in hex.
func (t *JoinToken) Prefix() string {
	n := PrefixByteLen
	if len(t.TokenHash) < n {
		n = len(t.TokenHash)
	}
	return hex.EncodeToString(t.TokenHash[:n])
}

// Consume marks the token used at the given time. Consuming twice fails
// with ErrTokenUsed; consuming at or past expiry fails with
// ErrTokenExpired — distinct codes so the handler can tell the operator
// whether to mint a fresh token or stop replaying one.
func (t *JoinToken) Consume(now time.Time) error {
	if t.UsedAt != nil {
		return fmt.Errorf("%w: consumed at %s", ErrTokenUsed, t.UsedAt.UTC().Format(time.RFC3339))
	}
	if !now.Before(t.ExpiresAt) {
		return fmt.Errorf("%w: expired at %s", ErrTokenExpired, t.ExpiresAt.UTC().Format(time.RFC3339))
	}
	used := now
	t.UsedAt = &used
	return nil
}

// GenerateHostID mints a server-side host identity for a join that did
// not bring its own: "host-" plus eight random hex characters (the T2
// contract). The rng is injected so tests pin the shape.
func GenerateHostID(rng io.Reader) (string, error) {
	buf := make([]byte, 4)
	if _, err := io.ReadFull(rng, buf); err != nil {
		return "", fmt.Errorf("topology: host id randomness: %w", err)
	}
	return "host-" + hex.EncodeToString(buf), nil
}
