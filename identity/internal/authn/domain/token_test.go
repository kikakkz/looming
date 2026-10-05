// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"time"
)

func TestGenerateToken(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rng := bytes.NewReader(make([]byte, 32))
	raw, tok, err := GenerateToken("p-1", 24*time.Hour, rng, now)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if raw == "" || len(tok.TokenHash) != sha256.Size {
		t.Fatalf("raw token and hash must be produced, got %q / %d bytes", raw, len(tok.TokenHash))
	}
	sum := sha256.Sum256([]byte(raw))
	if !bytes.Equal(tok.TokenHash, sum[:]) {
		t.Fatal("hash at rest must be sha256 of the raw token")
	}
	if tok.PrincipalID != "p-1" || !tok.IssuedAt.Equal(now) || !tok.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("unexpected token: %+v", tok)
	}
}

func TestGenerateTokenRejectsNonPositiveTTL(t *testing.T) {
	_, _, err := GenerateToken("p-1", 0, bytes.NewReader(make([]byte, 32)), time.Now())
	if err == nil {
		t.Fatal("zero ttl must fail")
	}
}

func TestValidateAt(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		tok     *Token
		at      time.Time
		wantErr error
	}{
		{"fresh", &Token{ExpiresAt: now.Add(time.Hour)}, now, nil},
		{"expired", &Token{ExpiresAt: now.Add(time.Hour)}, now.Add(2 * time.Hour), ErrTokenExpired},
		{"boundary is expired", &Token{ExpiresAt: now.Add(time.Hour)}, now.Add(time.Hour), ErrTokenExpired},
		{"revoked", &Token{ExpiresAt: now.Add(time.Hour), RevokedAt: timePtrAt(now)}, now, ErrTokenRevoked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.tok.ValidateAt(tc.at); err != tc.wantErr {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestRevoke(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tok := &Token{ExpiresAt: now.Add(time.Hour)}
	if err := tok.Revoke(now); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if tok.RevokedAt == nil || !tok.RevokedAt.Equal(now) {
		t.Fatalf("RevokedAt must be set, got %+v", tok.RevokedAt)
	}
	if err := tok.Revoke(now); err != ErrTokenRevoked {
		t.Fatalf("double revoke must fail with ErrTokenRevoked, got %v", err)
	}
	if err := tok.ValidateAt(now.Add(30 * time.Minute)); err != ErrTokenRevoked {
		t.Fatalf("revoked token must not validate, got %v", err)
	}
}

func timePtrAt(t time.Time) *time.Time { return &t }
