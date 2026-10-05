// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

var domainNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// fakeRand feeds GenerateKeySecret deterministic bytes: 0x00, 0x01, ... —
// the unit layer needs shape and repeatability, not unpredictability.
type fakeRand struct{ next byte }

func (f *fakeRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = f.next
		f.next++
	}
	return len(p), nil
}

func TestGenerateKeySecretFormat(t *testing.T) {
	secret, err := GenerateKeySecret(&fakeRand{})
	if err != nil {
		t.Fatalf("GenerateKeySecret: %v", err)
	}
	raw := string(secret)
	if !strings.HasPrefix(raw, "lk-") {
		t.Fatalf("secret must carry the lk- prefix, got %q", raw)
	}
	body := strings.TrimPrefix(raw, "lk-")
	if len(body) != 43 {
		t.Fatalf("base64url(32 bytes, no pad) is 43 chars, got %d", len(body))
	}
	if strings.ContainsAny(body, "+/=") {
		t.Fatalf("body must be unpadded base64url, got %q", body)
	}
	if _, err := base64.RawURLEncoding.DecodeString(body); err != nil {
		t.Fatalf("body must decode as raw URL base64: %v", err)
	}
}

func TestKeySecretHash(t *testing.T) {
	secret, err := GenerateKeySecret(&fakeRand{})
	if err != nil {
		t.Fatalf("GenerateKeySecret: %v", err)
	}
	got := secret.Hash()
	want := sha256.Sum256([]byte(secret))
	if got != want {
		t.Fatalf("Hash must be SHA-256 of the raw secret")
	}
	// Deterministic for the same secret, distinct across secrets.
	again := secret.Hash()
	if got != again {
		t.Fatal("Hash must be deterministic")
	}
	other, _ := GenerateKeySecret(&fakeRand{next: 200})
	if other.Hash() == got {
		t.Fatal("distinct secrets must hash distinctly")
	}
}

func TestKeySecretMask(t *testing.T) {
	secret, err := GenerateKeySecret(&fakeRand{})
	if err != nil {
		t.Fatalf("GenerateKeySecret: %v", err)
	}
	raw := string(secret)
	mask := secret.Mask()
	if !strings.HasPrefix(mask, "lk-…") {
		t.Fatalf("mask must open with the lk-… prefix, got %q", mask)
	}
	if !strings.HasSuffix(mask, raw[len(raw)-4:]) {
		t.Fatalf("mask must end with the raw secret's last 4 chars, got %q", mask)
	}
	if strings.Contains(mask, raw[3:len(raw)-4]) {
		t.Fatalf("mask must never expose the middle of the secret, got %q for %q", mask, raw)
	}
	if len(mask) != len("lk-…")+4 {
		t.Fatalf("mask shape is lk-…+last4, got %q", mask)
	}
}

func TestMaskBoundaryShortSecret(t *testing.T) {
	// A malformed/short secret must not panic the mask; it degrades to
	// the last 4 chars of whatever it holds (fuzz guard at the boundary).
	short := KeySecret("lk-ab")
	if got := short.Mask(); got != "lk-…"+string(short[len(short)-4:]) {
		t.Fatalf("short-secret mask mismatch: %q", got)
	}
	tiny := KeySecret("x")
	if got := tiny.Mask(); got != "lk-…x" {
		t.Fatalf("tiny-secret mask mismatch: %q", got)
	}
}

func TestNewLoomingKeyDerivations(t *testing.T) {
	secret, err := GenerateKeySecret(&fakeRand{})
	if err != nil {
		t.Fatalf("GenerateKeySecret: %v", err)
	}
	sealed := []byte("sealed-blob")
	k, err := NewLoomingKey("id-1", "principal-1", "ci bot", secret, sealed, domainNow)
	if err != nil {
		t.Fatalf("NewLoomingKey: %v", err)
	}
	if k.ID != "id-1" || k.PrincipalID != "principal-1" || k.Name != "ci bot" {
		t.Fatalf("identity fields mismatch: %+v", k)
	}
	if k.Status != StatusActive {
		t.Fatalf("fresh key must be active, got %q", k.Status)
	}
	if k.RevokedAt != nil {
		t.Fatal("fresh key must not carry a revocation time")
	}
	if k.CreatedAt != domainNow {
		t.Fatalf("created_at mismatch: %v", k.CreatedAt)
	}
	if k.Prefix != "lk-"+string(secret)[3:9] {
		t.Fatalf("prefix is lk- + first 6 of the raw body, got %q", k.Prefix)
	}
	if k.Last4 != string(secret)[len(secret)-4:] {
		t.Fatalf("last4 is the raw secret's last 4 chars, got %q", k.Last4)
	}
	if k.KeyHash != secret.Hash() {
		t.Fatal("KeyHash must be the secret's SHA-256")
	}
	if !bytes.Equal(k.Sealed, sealed) {
		t.Fatal("Sealed must roundtrip the adapter blob")
	}
}

func TestNewLoomingKeyNameBound(t *testing.T) {
	secret, _ := GenerateKeySecret(&fakeRand{})
	long := strings.Repeat("n", 129)
	if _, err := NewLoomingKey("id", "p", long, secret, nil, domainNow); err == nil {
		t.Fatal("name over 128 chars must fail")
	}
	k, err := NewLoomingKey("id", "p", strings.Repeat("n", 128), secret, nil, domainNow)
	if err != nil {
		t.Fatalf("128-char name is at the bound and must pass: %v", err)
	}
	if k.Name == "" {
		t.Fatal("name must be stored")
	}
}

func TestRevokeIsOneWay(t *testing.T) {
	secret, _ := GenerateKeySecret(&fakeRand{})
	k, _ := NewLoomingKey("id", "p", "", secret, nil, domainNow)
	revokedAt := domainNow.Add(time.Hour)
	if err := k.Revoke(revokedAt); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if k.Status != StatusRevoked {
		t.Fatalf("status must flip to revoked, got %q", k.Status)
	}
	if k.RevokedAt == nil || !k.RevokedAt.Equal(revokedAt) {
		t.Fatalf("RevokedAt must be set exactly once: %v", k.RevokedAt)
	}
	if err := k.Revoke(domainNow.Add(2 * time.Hour)); err == nil {
		t.Fatal("second revoke must fail")
	} else if err != ErrAlreadyRevoked {
		t.Fatalf("second revoke must be ErrAlreadyRevoked, got %v", err)
	}
	if !k.RevokedAt.Equal(revokedAt) {
		t.Fatal("RevokedAt must not move on a repeated revoke")
	}
}

func TestIssuanceAllowed(t *testing.T) {
	cases := []struct {
		recent, limit int
		want          bool
	}{
		{0, 10, true},
		{9, 10, true},
		{10, 10, false},
		{11, 10, false},
		{0, 1, true},
		{1, 1, false},
		{0, 0, false}, // a zero limit disables issuance outright
	}
	for _, tc := range cases {
		if got := IssuanceAllowed(tc.recent, tc.limit); got != tc.want {
			t.Fatalf("IssuanceAllowed(%d, %d) = %v, want %v", tc.recent, tc.limit, got, tc.want)
		}
	}
}
