// SPDX-License-Identifier: Apache-2.0

// Package domain holds the authn token model: hash-at-rest session
// tokens with expiry and one-way revocation. Pure model — stdlib only
// (AD-23/AD-24).
package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"
)

// Domain errors. ErrInvalidCredential deliberately covers unknown
// username, wrong password, and non-active principal — northbound
// failures must not distinguish which one it was (credential hygiene).
var (
	ErrInvalidCredential = errors.New("identity: invalid credentials")
	ErrTokenExpired      = errors.New("identity: token expired")
	ErrTokenRevoked      = errors.New("identity: token revoked")
	// ErrExternalAuthnNotSupported marks a VerifyExternalToken call in
	// a mode whose provider has no external authn (builtin-local
	// deployments). OIDC-mode deployments implement it; the error
	// exists so the port contract stays total.
	ErrExternalAuthnNotSupported = errors.New("identity: external authn not supported in this mode")
)

// Token is a session credential. The raw value is shown once at issue;
// only its SHA-256 hash is stored (identity-l1 §4 credential rule).
type Token struct {
	TokenHash   []byte
	PrincipalID string
	IssuedAt    time.Time
	ExpiresAt   time.Time
	RevokedAt   *time.Time
}

// GenerateToken mints a token: 32 random bytes, base64url raw, SHA-256
// hash at rest. rng and now are injected (AD-25).
func GenerateToken(principalID string, ttl time.Duration, rng io.Reader, now time.Time) (raw string, tok *Token, err error) {
	if ttl <= 0 {
		return "", nil, fmt.Errorf("identity: token ttl must be positive, got %s", ttl)
	}
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rng, buf); err != nil {
		return "", nil, fmt.Errorf("identity: token randomness: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, &Token{
		TokenHash:   HashRawToken(raw),
		PrincipalID: principalID,
		IssuedAt:    now,
		ExpiresAt:   now.Add(ttl),
	}, nil
}

// HashRawToken derives the at-rest hash for a raw token.
func HashRawToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// ValidateAt checks revocation and expiry at the given time.
func (t *Token) ValidateAt(now time.Time) error {
	if t.RevokedAt != nil {
		return ErrTokenRevoked
	}
	if !now.Before(t.ExpiresAt) {
		return ErrTokenExpired
	}
	return nil
}

// Revoke invalidates the token at the given time; revoking twice fails
// with ErrTokenRevoked (one-way lifecycle).
func (t *Token) Revoke(now time.Time) error {
	if t.RevokedAt != nil {
		return ErrTokenRevoked
	}
	revoked := now
	t.RevokedAt = &revoked
	return nil
}
