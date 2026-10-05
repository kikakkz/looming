// SPDX-License-Identifier: Apache-2.0

// Package domain holds the LoomingKey aggregate and the KeySecret value
// object (identity-l1 §2/§4). The storage model is dual-track: a
// SHA-256 hash feeds the validation path, an AES-GCM sealed blob
// (adapter-owned; only SHA-256 lives here) feeds the repeatable reveal.
// Pure model — stdlib only (AD-23/AD-24).
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

// KeyPrefix marks LoomingKey secrets; Prefix extends it with six body
// characters for list display, Mask with an ellipsis and the last four.
const KeyPrefix = "lk-"

// Display widths (identity-l1 §4: prefix + checksum for typo detection).
const (
	PrefixBodyLen = 6
	Last4Len      = 4
)

// keyByteLen is the raw entropy behind a secret; its unpadded base64url
// encoding is 43 characters.
const keyByteLen = 32

// NameMaxLen bounds the optional human label.
const NameMaxLen = 128

// Status is the key lifecycle: active → revoked, one-way.
type Status string

const (
	StatusActive  Status = "active"
	StatusRevoked Status = "revoked"
)

// Domain errors. Persistence-facing sentinels (ErrNotFound, ErrConflict)
// live here because uniqueness and one-way-revoke guards are aggregate
// invariants, not adapter details (same precedent as the principal
// aggregate).
var (
	ErrInvalidName     = errors.New("identity: key name too long")
	ErrAlreadyRevoked  = errors.New("identity: key already revoked")
	ErrNotFound        = errors.New("identity: key not found")
	ErrConflict        = errors.New("identity: conflicting key write")
	errShortSecretBody = errors.New("identity: key secret body too short")
)

// KeySecret is a raw LoomingKey token: "lk-" + unpadded base64url of 32
// random bytes (~46 chars). It exists only in memory and in the issue /
// reveal responses — at rest the aggregate keeps its SHA-256 hash and
// the adapter's sealed blob.
type KeySecret string

// GenerateKeySecret mints a secret from rng. rng must be a CSPRNG in
// production (AD-25: injected, never a global).
func GenerateKeySecret(rng io.Reader) (KeySecret, error) {
	raw := make([]byte, keyByteLen)
	if _, err := io.ReadFull(rng, raw); err != nil {
		return "", fmt.Errorf("identity: generate key secret: %w", err)
	}
	return KeySecret(KeyPrefix + base64.RawURLEncoding.EncodeToString(raw)), nil
}

// Hash is the SHA-256 validation digest stored at rest.
func (k KeySecret) Hash() [32]byte {
	return sha256.Sum256([]byte(k))
}

// Mask renders the Stripe/GitHub-style display form: the lk-… prefix
// plus the secret's last four characters. The middle never appears.
func (k KeySecret) Mask() string {
	raw := string(k)
	last := raw
	if len(raw) > Last4Len {
		last = raw[len(raw)-Last4Len:]
	}
	return KeyPrefix + "…" + last
}

// LoomingKey is the API-key aggregate root (identity-l1 §4). KeyHash and
// Sealed are the dual-track at-rest representation: hash for the
// gateway validation path, sealed blob for the reveal path. Neither
// ever leaves the service northbound.
type LoomingKey struct {
	ID          string
	PrincipalID string
	Name        string
	Prefix      string
	Last4       string
	Status      Status
	KeyHash     [32]byte
	Sealed      []byte
	CreatedAt   time.Time
	RevokedAt   *time.Time
}

// NewLoomingKey derives a fresh active key from an already-sealed
// secret. sealed is opaque here — the sealer adapter produced it and
// only the reveal path reads it back.
func NewLoomingKey(id, principalID, name string, secret KeySecret, sealed []byte, now time.Time) (*LoomingKey, error) {
	if len(name) > NameMaxLen {
		return nil, fmt.Errorf("%w: %d chars", ErrInvalidName, len(name))
	}
	raw := string(secret)
	body, ok := strings.CutPrefix(raw, KeyPrefix)
	if !ok || len(body) < PrefixBodyLen {
		return nil, fmt.Errorf("%w", errShortSecretBody)
	}
	k := &LoomingKey{
		ID:          id,
		PrincipalID: principalID,
		Name:        name,
		Prefix:      KeyPrefix + body[:PrefixBodyLen],
		Status:      StatusActive,
		KeyHash:     secret.Hash(),
		Sealed:      sealed,
		CreatedAt:   now,
	}
	if len(raw) >= Last4Len {
		k.Last4 = raw[len(raw)-Last4Len:]
	}
	return k, nil
}

// Revoke flips the key to revoked exactly once. A repeated revoke — or a
// revoke racing another one — fails with ErrAlreadyRevoked, the domain's
// not-found-or-already-revoked sentinel shared with the repository.
func (k *LoomingKey) Revoke(now time.Time) error {
	if k.Status == StatusRevoked {
		return ErrAlreadyRevoked
	}
	k.Status = StatusRevoked
	k.RevokedAt = &now
	return nil
}

// IssuanceAllowed is the pure rate-limit rule checked by the service:
// issuance proceeds only while the recent count stays strictly below the
// limit, so a limit of zero disables issuance outright.
func IssuanceAllowed(recent, limit int) bool {
	return recent < limit
}
