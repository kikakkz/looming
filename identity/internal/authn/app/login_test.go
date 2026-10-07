// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"errors"
	"testing"
	"time"

	authndomain "github.com/kikakkz/looming/identity/internal/authn/domain"
	"github.com/kikakkz/looming/identity/internal/authn/port"
)

type fakeProvider struct {
	principalID         string
	verifyErr           error
	raw                 string
	issueErr            error
	issueFor            string
	externalPrincipalID string
	externalErr         error
	externalSeen        string
}

func (f *fakeProvider) VerifyPassword(_ context.Context, _, _ string) (string, error) {
	if f.verifyErr != nil {
		return "", f.verifyErr
	}
	return f.principalID, nil
}

func (f *fakeProvider) VerifyExternalToken(_ context.Context, raw string) (string, error) {
	f.externalSeen = raw
	if f.externalErr != nil {
		return "", f.externalErr
	}
	return f.externalPrincipalID, nil
}

func (f *fakeProvider) Issue(_ context.Context, principalID string) (string, error) {
	if f.issueErr != nil {
		return "", f.issueErr
	}
	f.issueFor = principalID
	return f.raw, nil
}

func (f *fakeProvider) Validate(context.Context, string) (port.TokenInfo, error) {
	return port.TokenInfo{}, nil
}

func (f *fakeProvider) Revoke(context.Context, string) error { return nil }

func TestLoginSuccess(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	prov := &fakeProvider{principalID: "p-1", raw: "raw-token"}
	svc := NewLoginService(prov, 24*time.Hour, func() time.Time { return now })
	raw, expiresAt, err := svc.Login(context.Background(), "ker", "whatever")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if raw != "raw-token" {
		t.Fatalf("want raw token, got %q", raw)
	}
	if !expiresAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("expires_at must be issue time + ttl, got %s", expiresAt)
	}
	if prov.issueFor != "p-1" {
		t.Fatalf("token must be issued for the verified principal, got %q", prov.issueFor)
	}
}

func TestLoginVerifyFailure(t *testing.T) {
	prov := &fakeProvider{verifyErr: authndomain.ErrInvalidCredential}
	svc := NewLoginService(prov, time.Hour, time.Now)
	_, _, err := svc.Login(context.Background(), "ker", "wrong")
	if !errors.Is(err, authndomain.ErrInvalidCredential) {
		t.Fatalf("want ErrInvalidCredential, got %v", err)
	}
	if prov.issueFor != "" {
		t.Fatal("a failed verify must not issue a token")
	}
}

func TestLoginIssueFailure(t *testing.T) {
	prov := &fakeProvider{principalID: "p-1", issueErr: errors.New("db down")}
	svc := NewLoginService(prov, time.Hour, time.Now)
	_, _, err := svc.Login(context.Background(), "ker", "fine-password")
	if err == nil {
		t.Fatal("issue failure must propagate")
	}
}

func TestLoginExternalFlow(t *testing.T) {
	prov := &fakeProvider{externalPrincipalID: "p-oidc-1", raw: "session-token"}
	svc := NewLoginService(prov, time.Hour, time.Now)
	raw, expiresAt, err := svc.LoginExternal(context.Background(), "id-token")
	if err != nil {
		t.Fatalf("LoginExternal: %v", err)
	}
	if raw == "" || expiresAt.IsZero() {
		t.Fatalf("token and expiry must be populated: %q %v", raw, expiresAt)
	}
	if prov.externalSeen != "id-token" {
		t.Fatalf("provider must receive the raw id token, got %q", prov.externalSeen)
	}
	if prov.issueFor != "p-oidc-1" {
		t.Fatalf("token must be issued for the external principal, got %q", prov.issueFor)
	}
}

func TestLoginExternalVerifyFailure(t *testing.T) {
	prov := &fakeProvider{externalErr: authndomain.ErrInvalidCredential}
	svc := NewLoginService(prov, time.Hour, time.Now)
	_, _, err := svc.LoginExternal(context.Background(), "bad-token")
	if !errors.Is(err, authndomain.ErrInvalidCredential) {
		t.Fatalf("want ErrInvalidCredential, got %v", err)
	}
	if prov.issueFor != "" {
		t.Fatal("a failed external verify must not issue a token")
	}
}

// steppingClock advances on every read after the first, so a second
// clock read would observe a later time than the first.
type steppingClock struct {
	base time.Time
	step time.Duration
	n    int
}

func (c *steppingClock) now() time.Time {
	c.n++
	if c.n > 1 {
		c.base = c.base.Add(c.step)
	}
	return c.base
}

func TestLoginReportsConservativeExpiry(t *testing.T) {
	start := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	clock := &steppingClock{base: start, step: time.Hour}
	prov := &fakeProvider{principalID: "p-1", raw: "raw"}
	svc := NewLoginService(prov, 24*time.Hour, clock.now)
	_, expiresAt, err := svc.Login(context.Background(), "ker", "fine-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	// Expiry must derive from the read taken before Issue, even though
	// the clock has since advanced by an hour.
	if !expiresAt.Equal(start.Add(24 * time.Hour)) {
		t.Fatalf("want conservative expiry %s, got %s", start.Add(24*time.Hour), expiresAt)
	}
}
