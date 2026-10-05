// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestGenerateInvite(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rng := bytes.NewReader(make([]byte, 32))
	raw, tok, err := GenerateInvite("admin-1", time.Hour, rng, now)
	if err != nil {
		t.Fatalf("GenerateInvite: %v", err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("raw token must be base64url: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("raw token must decode to 32 bytes, got %d", len(decoded))
	}
	sum := sha256.Sum256([]byte(raw))
	if !bytes.Equal(tok.TokenHash, sum[:]) {
		t.Fatal("hash at rest must be sha256 of the raw token")
	}
	if tok.CreatedBy != "admin-1" || !tok.CreatedAt.Equal(now) || !tok.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("unexpected metadata: %+v", tok)
	}
	if tok.UsedAt != nil {
		t.Fatal("fresh invite must be unused")
	}
}

func TestGenerateInviteShortRandomness(t *testing.T) {
	rng := bytes.NewReader(make([]byte, 8))
	_, _, err := GenerateInvite("admin-1", time.Hour, rng, time.Now())
	if err == nil {
		t.Fatal("short randomness must fail")
	}
}

func TestGenerateInviteRejectsNonPositiveTTL(t *testing.T) {
	_, _, err := GenerateInvite("admin-1", 0, bytes.NewReader(make([]byte, 32)), time.Now())
	if err == nil {
		t.Fatal("zero ttl must fail")
	}
}

func TestInviteConsume(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		tok     *InviteToken
		at      time.Time
		wantErr error
	}{
		{
			"fresh consume",
			&InviteToken{ExpiresAt: now.Add(time.Hour)},
			now,
			nil,
		},
		{
			"expired",
			&InviteToken{ExpiresAt: now.Add(time.Hour)},
			now.Add(2 * time.Hour),
			ErrInvalidInvite,
		},
		{
			"already used",
			&InviteToken{ExpiresAt: now.Add(time.Hour), UsedAt: timePtr(now)},
			now.Add(30 * time.Minute),
			ErrInvalidInvite,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tok.Consume(tc.at)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
			if tc.wantErr == nil && (tc.tok.UsedAt == nil || !tc.tok.UsedAt.Equal(tc.at)) {
				t.Fatalf("UsedAt must be set to consume time, got %+v", tc.tok.UsedAt)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time { return &t }
