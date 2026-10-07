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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"

	authndomain "github.com/kikakkz/looming/identity/internal/authn/domain"
	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
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
	// sight of a verified identity — strictly opt-in (the security
	// default; CodeRabbit review on PR #141).
	AutoRegister bool
	// Insecure permits http issuers and JWKS URLs for loopback-only
	// dev IdPs (the house trusted-network opt-out pattern); anything
	// cross-host must use https.
	Insecure bool
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
	bindings      authnport.BindingRepository
	issuer        string
	usernameClaim string
	autoRegister  bool
	clock         func() time.Time
}

// NewOIDCProvider wires the provider. The issuer's well-known
// configuration is fetched at construction: a misconfigured IdP must
// fail identityd's boot, not the first login (fail-fast, house rule).
// Transport is authenticated by default: http issuers/JWKS URLs and
// https→http redirect downgrades are rejected unless cfg.Insecure
// (loopback dev IdPs only — the trusted-network opt-out pattern;
// CodeRabbit security review on PR #141). db/ttl/rng/clock feed the
// embedded LocalProvider.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig, db *sql.DB, ttl time.Duration, rng io.Reader, clock func() time.Time, repo principalport.Repository, bindings authnport.BindingRepository) (*OIDCProvider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, errors.New("identity: oidc issuer and client_id are required")
	}
	claim := cfg.UsernameClaim
	if claim == "" {
		claim = "email"
	}
	var discoveryCtx context.Context
	if !cfg.Insecure {
		if err := requireHTTPS(cfg.Issuer, "issuer"); err != nil {
			return nil, err
		}
		discoveryCtx = oidc.ClientContext(ctx, secureHTTPClient())
	} else {
		discoveryCtx = oidc.InsecureIssuerURLContext(ctx, cfg.Issuer)
	}
	provider, err := oidc.NewProvider(discoveryCtx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("identity: oidc discovery %q: %w", cfg.Issuer, err)
	}
	if !cfg.Insecure {
		if err := checkJWKSURL(provider); err != nil {
			return nil, err
		}
	}
	verifier := goOIDCVerifier{inner: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})}
	return &OIDCProvider{
		LocalProvider: NewLocalProvider(db, ttl, rng, clock),
		verifier:      verifier,
		repo:          repo,
		bindings:      bindings,
		issuer:        cfg.Issuer,
		usernameClaim: claim,
		autoRegister:  cfg.AutoRegister,
		clock:         clock,
	}, nil
}

// requireHTTPS rejects non-https endpoints with a clear error.
func requireHTTPS(rawURL, what string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("identity: oidc %s must be https (got %q); set IDENTITY_OIDC_INSECURE=1 only for loopback dev IdPs", what, rawURL)
	}
	return nil
}

// checkJWKSURL validates the discovered jwks_uri: an on-path attacker
// who tampers with discovery could advertise an http key URL and feed
// the verifier attacker-chosen keys (CWE-319).
func checkJWKSURL(provider *oidc.Provider) error {
	claims := struct {
		JWKSURL string `json:"jwks_uri"`
	}{}
	if err := provider.Claims(&claims); err != nil {
		return fmt.Errorf("identity: oidc discovery claims: %w", err)
	}
	return requireHTTPS(claims.JWKSURL, "jwks_uri")
}

// secureHTTPClient refuses redirects that downgrade to http, so the
// discovery fetch cannot be walked onto an insecure origin.
func secureHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("identity: oidc redirect to non-https URL %q refused", req.URL.Redacted())
			}
			if len(via) >= 10 {
				return errors.New("identity: oidc too many redirects")
			}
			return nil
		},
	}
}

// VerifyPassword is not offered in OIDC mode: local credentials are
// the builtin provider's mode (identity-l1 §5).
func (o *OIDCProvider) VerifyPassword(_ context.Context, _, _ string) (string, error) {
	return "", authndomain.ErrExternalAuthnNotSupported
}

// VerifyExternalToken validates the OIDC ID token and resolves it to
// a principal through the immutable issuer+subject binding — never
// through username derivation, which is a display/registration
// attribute only (two IdP accounts can derive the same username; the
// binding guarantees they never share a principal, CodeRabbit security
// review on PR #141). A verified identity with no binding registers
// per policy; disabled principals fail closed like every authn path.
func (o *OIDCProvider) VerifyExternalToken(ctx context.Context, rawToken string) (string, error) {
	identity, err := o.verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", authndomain.ErrInvalidCredential
	}
	principalID, err := o.bindings.ByIssuerSubject(ctx, o.issuer, identity.Subject)
	if errors.Is(err, principaldomain.ErrNotFound) {
		if !o.autoRegister {
			return "", authndomain.ErrInvalidCredential
		}
		return o.register(ctx, identity)
	}
	if err != nil {
		return "", err
	}
	p, err := o.repo.ByID(ctx, principalID)
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
// auto-registration policy), then binds the issuer+subject pair to
// it. The username derives from the configured claim; a collision
// with an existing principal (another IdP account deriving the same
// name) takes a subject suffix — display attribute, not identity.
func (o *OIDCProvider) register(ctx context.Context, identity VerifiedIdentity) (string, error) {
	display := identity.Claim("name")
	if display == "" {
		display = identity.Claim(o.usernameClaim)
	}
	username := authndomain.DeriveUsername(identity.Claim(o.usernameClaim), identity.Subject)
	if username == "" {
		return "", authndomain.ErrInvalidCredential
	}
	now := o.clock()
	for attempt := 0; ; attempt++ {
		candidate := username
		if attempt > 0 {
			suffix := identity.Subject
			if len(suffix) > 8 {
				suffix = suffix[:8]
			}
			candidate = fmt.Sprintf("%s-%s", username, strings.ToLower(suffix))
			if len(candidate) > authndomain.UsernameMaxLen {
				candidate = candidate[:authndomain.UsernameMaxLen]
				candidate = strings.TrimRight(candidate, "._-")
			}
		}
		p, err := principaldomain.NewRegistration(
			uuid.NewString(), candidate, principaldomain.KindHuman, display, "",
			principaldomain.StatusActive, now)
		if err != nil {
			return "", fmt.Errorf("identity: oidc auto-register: %w", err)
		}
		err = o.repo.Create(ctx, p)
		if errors.Is(err, principaldomain.ErrUsernameTaken) && attempt == 0 {
			continue // attempt 2 appends the subject suffix
		}
		if err != nil {
			return "", fmt.Errorf("identity: oidc auto-register persist: %w", err)
		}
		if err := o.bindings.Create(ctx, o.issuer, identity.Subject, p.ID); err != nil {
			return "", fmt.Errorf("identity: oidc binding persist: %w", err)
		}
		return p.ID, nil
	}
}
