// SPDX-License-Identifier: Apache-2.0

// Package app holds the authn use cases: the login flow that turns
// local credentials into a session token.
package app

import (
	"context"
	"time"

	"github.com/kikakkz/looming/identity/internal/authn/port"
)

// LoginService runs the login journey: verify credentials, then issue
// a session token.
type LoginService struct {
	provider port.Provider
	ttl      time.Duration
	clock    func() time.Time
}

// NewLoginService wires the service. ttl is the same configured token
// lifetime the provider issues with — composition injects one config
// value into both; the service mirrors it so the login response can
// state the expiry without re-reading storage.
func NewLoginService(provider port.Provider, ttl time.Duration, clock func() time.Time) *LoginService {
	return &LoginService{provider: provider, ttl: ttl, clock: clock}
}

// Login verifies the credentials and, on success, returns the raw
// token and its expiry. Any verify or issue failure denies the login
// with the provider's error (domain.ErrInvalidCredential and friends).
// The expiry is computed from a clock read taken just before Issue:
// the reported validity can undershoot the stored token's real expiry,
// never overshoot it.
func (s *LoginService) Login(ctx context.Context, username, password string) (rawToken string, expiresAt time.Time, err error) {
	principalID, err := s.provider.VerifyPassword(ctx, username, password)
	if err != nil {
		return "", time.Time{}, err
	}
	return s.issue(ctx, principalID)
}

// LoginExternal is the OIDC-mode login: an IdP-issued ID token stands
// in for local credentials (the provider verifies it and resolves —
// possibly auto-registers — the principal), and the session machinery
// is identical from there on. Deployments on the builtin provider
// fail with domain.ErrExternalAuthnNotSupported.
func (s *LoginService) LoginExternal(ctx context.Context, idToken string) (rawToken string, expiresAt time.Time, err error) {
	principalID, err := s.provider.VerifyExternalToken(ctx, idToken)
	if err != nil {
		return "", time.Time{}, err
	}
	return s.issue(ctx, principalID)
}

// issue mints the session token for an authenticated principal and
// reports its expiry.
func (s *LoginService) issue(ctx context.Context, principalID string) (rawToken string, expiresAt time.Time, err error) {
	issuedAt := s.clock()
	raw, err := s.provider.Issue(ctx, principalID)
	if err != nil {
		return "", time.Time{}, err
	}
	return raw, issuedAt.Add(s.ttl), nil
}
