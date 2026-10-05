// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kikakkz/looming/identity/internal/authn/domain"
	"github.com/kikakkz/looming/identity/internal/authn/port"
)

type fakeProvider struct {
	principalID string
	verifyErr   error
	raw         string
	issueErr    error
	issueFor    string
}

func (f *fakeProvider) VerifyPassword(_ context.Context, _, _ string) (string, error) {
	if f.verifyErr != nil {
		return "", f.verifyErr
	}
	return f.principalID, nil
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
	prov := &fakeProvider{verifyErr: domain.ErrInvalidCredential}
	svc := NewLoginService(prov, time.Hour, time.Now)
	_, _, err := svc.Login(context.Background(), "ker", "wrong")
	if !errors.Is(err, domain.ErrInvalidCredential) {
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
