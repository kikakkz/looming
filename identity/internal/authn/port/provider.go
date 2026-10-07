// SPDX-License-Identifier: Apache-2.0

// Package port defines the authn contract: the provider implementations
// (builtin local, later OIDC) and the validated-session type the HTTP
// layer carries in request context.
package port

import (
	"context"
	"time"
)

// TokenInfo is a validated session's claims.
type TokenInfo struct {
	PrincipalID string
	Roles       []string
	ExpiresAt   time.Time
}

// Provider is the AuthNProvider port (identity-l1 §5). The builtin
// local adapter implements it over postgres; the OIDC adapter
// (slice E) is the same-contract second implementation. Error
// contract: domain.ErrInvalidCredential for VerifyPassword and
// Validate failures (unknown/expired/revoked tokens map there too),
// domain.ErrTokenExpired and domain.ErrTokenRevoked surface from
// Validate for observability.
type Provider interface {
	// VerifyPassword checks local credentials and returns the
	// principal ID. Unknown username, wrong password, and non-active
	// principal all fail with domain.ErrInvalidCredential. Modes
	// without local credentials (OIDC) fail with
	// domain.ErrExternalAuthnNotSupported.
	VerifyPassword(ctx context.Context, username, password string) (principalID string, err error)
	// VerifyExternalToken validates an OIDC ID token (or an
	// equivalent enterprise IdP artifact) and returns the principal
	// ID, provisioning the principal on first sight when the mode
	// mandates auto-registration. Modes without external authn fail
	// with domain.ErrExternalAuthnNotSupported.
	VerifyExternalToken(ctx context.Context, rawToken string) (principalID string, err error)
	// Issue mints a session token for an authenticated principal and
	// returns the raw value once.
	Issue(ctx context.Context, principalID string) (rawToken string, err error)
	// Validate resolves a raw token to its claims.
	Validate(ctx context.Context, rawToken string) (TokenInfo, error)
	// Revoke invalidates a raw token (one-way). Revoking an unknown
	// token fails with domain.ErrInvalidCredential; an already-revoked
	// token revokes idempotently.
	Revoke(ctx context.Context, rawToken string) error
}

// Request-context plumbing for the validated session. The middleware
// stores a TokenInfo after successful validation; downstream handlers
// read it back. Defined here because TokenInfo is this port's product.
type ctxKey struct{}

// WithTokenInfo stores the validated session in ctx.
func WithTokenInfo(ctx context.Context, info TokenInfo) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}

// BindingRepository persists the immutable issuer+subject → principal
// mapping (OIDC mode, slice E). The binding — not the derived
// username — is the ownership authority: username derivation is a
// display/registration attribute only.
type BindingRepository interface {
	// ByIssuerSubject resolves the bound principal ID or
	// principaldomain.ErrNotFound.
	ByIssuerSubject(ctx context.Context, issuer, subject string) (string, error)
	// Create binds a verified identity to a principal. A raced
	// duplicate surfaces as the driver's unique-violation error.
	Create(ctx context.Context, issuer, subject, principalID string) error
}

// TokenInfoFrom returns the validated session, or false when the
// request was not authenticated.
func TokenInfoFrom(ctx context.Context) (TokenInfo, bool) {
	info, ok := ctx.Value(ctxKey{}).(TokenInfo)
	return info, ok
}
