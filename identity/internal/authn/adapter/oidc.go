// SPDX-License-Identifier: Apache-2.0

// OIDC provider: the slice-E AuthNProvider adapter (identity-l1 §5 —
// "OIDC (Keycloak-compatible; OA systems as same-contract
// adapters)"). Session token issue/validate/revoke inherit the
// builtin local implementation (the session store is the same
// postgres table regardless of how the principal authenticated);
// password verification is not offered in this mode, and external
// token verification resolves the principal over OIDC ID tokens,
// auto-registering on first sight when the deployment opts in.
package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"

	authndomain "github.com/kikakkz/looming/identity/internal/authn/domain"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	principalport "github.com/kikakkz/looming/identity/internal/principal/port"
)

// OIDCConfig is the deployment-time wiring for the OIDC provider.
type OIDCConfig struct {
	// Issuer is the IdP's base URL; go-oidc discovers the well-known
	// configuration and JWKS from it (Keycloak-compatible:
	// {issuer}/.well-known/openid-configuration).
	Issuer string
	// ClientID is the audience the ID tokens must carry.
	ClientID string
	// UsernameClaim selects the claim the principal username derives
	// from ("email" default; "preferred_username" and "sub" are the
	// usual Keycloak alternatives).
	UsernameClaim string
	// AutoRegister provisions an active member principal on first
	// sight of a verified identity (Gitea's OIDC precedent): the IdP
	// already authenticated the user, and org-level admission stays
	// the IdP's group-policy job.
	AutoRegister bool
}

// TokenVerifier abstracts go-oidc's verifier for unit tests: the
// production shape is one method, and faking it avoids standing up a
// JWKS endpoint in every test.
type TokenVerifier interface {
	Verify(ctx context.Context, rawIDToken string) (VerifiedIdentity, error)
}

// VerifiedIdentity is the post-verification claim view the provider
// consumes — a narrow, adapter-local shape (not go-oidc's IDToken) so
// tests and future non-OIDC IdP adapters stay cheap.
type VerifiedIdentity struct {
	Subject string
	Claims  map[string]any
}

// Claim returns the named claim as a string (empty when absent or
// non-string).
func (v VerifiedIdentity) Claim(name string) string {
	raw, ok := v.Claims[name]
	if !ok {
		return ""
	}
	s, _ := raw.(string)
	return s
}

// goOIDCVerifier is the production TokenVerifier over go-oidc.
type goOIDCVerifier struct {
	inner *oidc.IDTokenVerifier
}

func (v goOIDCVerifier) Verify(ctx context.Context, raw string) (VerifiedIdentity, error) {
	idToken, err := v.inner.Verify(ctx, raw)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	claims := map[string]any{}
	if err := idToken.Claims(&claims); err != nil {
		return VerifiedIdentity{}, fmt.Errorf("identity: oidc claims: %w", err)
	}
	return VerifiedIdentity{Subject: idToken.Subject, Claims: claims}, nil
}

// OIDCProvider implements the AuthNProvider port for OIDC-mode
// deployments. The embedded LocalProvider carries the session token
// machinery; external verification resolves (and, per policy,
// provisions) principals via the principal capability's port.
type OIDCProvider struct {
	*LocalProvider
	verifier      TokenVerifier
	repo          principalport.Repository
	usernameClaim string
	autoRegister  bool
	clock         func() time.Time
}

// NewOIDCProvider wires the provider. The issuer's well-known
// configuration is fetched at construction: a misconfigured IdP must
// fail identityd's boot, not the first login (fail-fast, house rule).
// db/ttl/rng/clock feed the embedded LocalProvider.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig, db *sql.DB, ttl time.Duration, rng io.Reader, clock func() time.Time, repo principalport.Repository) (*OIDCProvider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, errors.New("identity: oidc issuer and client_id are required")
	}
	claim := cfg.UsernameClaim
	if claim == "" {
		claim = "email"
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("identity: oidc discovery %q: %w", cfg.Issuer, err)
	}
	verifier := goOIDCVerifier{inner: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})}
	return &OIDCProvider{
		LocalProvider: NewLocalProvider(db, ttl, rng, clock),
		verifier:      verifier,
		repo:          repo,
		usernameClaim: claim,
		autoRegister:  cfg.AutoRegister,
		clock:         clock,
	}, nil
}

// VerifyPassword is not offered in OIDC mode: local credentials are
// the builtin provider's mode (identity-l1 §5).
func (o *OIDCProvider) VerifyPassword(_ context.Context, _, _ string) (string, error) {
	return "", authndomain.ErrExternalAuthnNotSupported
}

// VerifyExternalToken validates the OIDC ID token and resolves it to
// a principal. The username derives from the configured claim; a
// principal the deployment has never seen is auto-registered per
// policy. Disabled principals fail closed like every other authn
// path.
func (o *OIDCProvider) VerifyExternalToken(ctx context.Context, rawToken string) (string, error) {
	identity, err := o.verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", authndomain.ErrInvalidCredential
	}
	username := authndomain.DeriveUsername(identity.Claim(o.usernameClaim), identity.Subject)
	if username == "" {
		return "", authndomain.ErrInvalidCredential
	}
	p, err := o.repo.ByUsername(ctx, username)
	if errors.Is(err, principaldomain.ErrNotFound) {
		if !o.autoRegister {
			return "", authndomain.ErrInvalidCredential
		}
		return o.register(ctx, username, identity)
	}
	if err != nil {
		return "", err
	}
	if p.Status != principaldomain.StatusActive {
		return "", authndomain.ErrInvalidCredential
	}
	return p.ID, nil
}

// register provisions the first-sight principal: active member (the
// IdP authenticated the user; org admission is the IdP's job per the
// auto-registration policy). Username collisions with a local user
// are impossible — DeriveUsername was ByUsername-missed just before.
func (o *OIDCProvider) register(ctx context.Context, username string, identity VerifiedIdentity) (string, error) {
	display := identity.Claim("name")
	if display == "" {
		display = identity.Claim(o.usernameClaim)
	}
	now := o.clock()
	p, err := principaldomain.NewRegistration(
		uuid.NewString(), username, principaldomain.KindHuman, display, "",
		principaldomain.StatusActive, now)
	if err != nil {
		return "", fmt.Errorf("identity: oidc auto-register: %w", err)
	}
	if err := o.repo.Create(ctx, p); err != nil {
		return "", fmt.Errorf("identity: oidc auto-register persist: %w", err)
	}
	return p.ID, nil
}
