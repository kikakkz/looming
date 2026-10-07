// SPDX-License-Identifier: Apache-2.0

// Command identityd runs the identity component: it migrates the
// schema and serves the v1 self/admin/bootstrap HTTP API. The first
// admin arrives through the one-shot bootstrap-invite flow
// (POST /v1/bootstrap/invite, topology-l1 §4) — there is deliberately
// no env-seeded admin. Wiring only — every decision lives in the
// capability packages (AD-23).
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver

	authnadapter "github.com/kikakkz/looming/identity/internal/authn/adapter"
	authnapp "github.com/kikakkz/looming/identity/internal/authn/app"
	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	gatewayfeedadapter "github.com/kikakkz/looming/identity/internal/gatewayfeed/adapter"
	gatewayfeedapp "github.com/kikakkz/looming/identity/internal/gatewayfeed/app"
	keyadapter "github.com/kikakkz/looming/identity/internal/key/adapter"
	keyapp "github.com/kikakkz/looming/identity/internal/key/app"
	policyadapter "github.com/kikakkz/looming/identity/internal/policy/adapter"
	policyapp "github.com/kikakkz/looming/identity/internal/policy/app"
	principaladapter "github.com/kikakkz/looming/identity/internal/principal/adapter"
	principalapp "github.com/kikakkz/looming/identity/internal/principal/app"
	provisionadapter "github.com/kikakkz/looming/identity/internal/provision/adapter"
	"github.com/kikakkz/looming/identity/internal/provision/adapter/litellm"
	provisionapp "github.com/kikakkz/looming/identity/internal/provision/app"
	provisionport "github.com/kikakkz/looming/identity/internal/provision/port"
	quotaadapter "github.com/kikakkz/looming/identity/internal/quota/adapter"
	quotaapp "github.com/kikakkz/looming/identity/internal/quota/app"
	"github.com/kikakkz/looming/identity/migrations"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log) // app-layer error logging lands in the same handler
	if err := run(log); err != nil {
		log.Error("identityd exited", "err", err)
		os.Exit(1)
	}
}

// config is the environment-driven configuration (IDENTITY_ prefix).
type config struct {
	listen            string
	databaseURL       string
	bootstrapKey      string
	tokenTTL          time.Duration
	argonConcurrency  int
	keyIssueLimit     int
	keyIssueWindow    time.Duration
	keyMasterKey      []byte
	gatewayToken      string
	watchTimeout      time.Duration
	engineURL         string
	engineKey         string
	engineName        string
	authnMode         string
	oidcIssuer        string
	oidcClientID      string
	oidcUsernameClaim string
	oidcAutoRegister  bool
	oidcInsecure      bool
}

// defaultArgonConcurrency bounds simultaneous argon2id operations on
// the unauthenticated routes (64 MiB of memory each).
const defaultArgonConcurrency = 16

// serverWriteTimeout bounds response writes; the feed watch must hold
// strictly below it or every held watch response would miss the
// deadline (validated against IDENTITY_WATCH_TIMEOUT in loadConfig).
const serverWriteTimeout = 60 * time.Second

const (
	defaultKeyIssueLimit  = 10
	defaultKeyIssueWindow = 24 * time.Hour
	defaultWatchTimeout   = 30 * time.Second
	// defaultEngineName identifies the phase-1 engine deployment in
	// identity_map and the feed's credential join; overridable when a
	// deployment fronts several engines later.
	defaultEngineName = "litellm"
	// engineHTTPTimeout bounds one engine admin call (provision,
	// budget, delete) so a hung engine cannot park an issuance.
	engineHTTPTimeout = 15 * time.Second
)

func loadConfig() (config, error) {
	cfg := config{
		listen:           os.Getenv("IDENTITY_LISTEN"),
		databaseURL:      os.Getenv("IDENTITY_DATABASE_URL"),
		bootstrapKey:     os.Getenv("IDENTITY_BOOTSTRAP_KEY"),
		tokenTTL:         24 * time.Hour,
		argonConcurrency: defaultArgonConcurrency,
		keyIssueLimit:    defaultKeyIssueLimit,
		keyIssueWindow:   defaultKeyIssueWindow,
		gatewayToken:     os.Getenv("IDENTITY_GATEWAY_TOKEN"),
		watchTimeout:     defaultWatchTimeout,
	}
	if cfg.listen == "" {
		cfg.listen = ":8080"
	}
	if cfg.databaseURL == "" {
		return config{}, errors.New("missing required config: IDENTITY_DATABASE_URL")
	}
	if cfg.gatewayToken == "" {
		return config{}, errors.New("missing required config: IDENTITY_GATEWAY_TOKEN")
	}
	var err error
	if cfg.tokenTTL, err = envDuration("IDENTITY_TOKEN_TTL", cfg.tokenTTL); err != nil {
		return config{}, err
	}
	if keyCfgErr := loadKeyConfig(&cfg); keyCfgErr != nil {
		return config{}, keyCfgErr
	}
	if cfg.watchTimeout, err = envDuration("IDENTITY_WATCH_TIMEOUT", cfg.watchTimeout); err != nil {
		return config{}, err
	}
	// A held watch must flush before the server's write deadline, and
	// a non-positive timeout would release every watch immediately.
	if cfg.watchTimeout <= 0 || cfg.watchTimeout >= serverWriteTimeout {
		return config{}, fmt.Errorf("IDENTITY_WATCH_TIMEOUT must be in (0, %s)", serverWriteTimeout)
	}
	if err := loadEngineConfig(&cfg); err != nil {
		return config{}, err
	}
	if err := loadAuthnConfig(&cfg); err != nil {
		return config{}, err
	}
	if err := loadRuntimeLimits(&cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// loadRuntimeLimits parses the concurrency knobs: the issuance rate
// limit and the argon2id permit pool. loadKeyConfig re-reads the issue
// limit for its window pairing; a drift between the two parses would
// contradict, so the limit is parsed once here and loadKeyConfig reads
// the env only for the window.
func loadRuntimeLimits(cfg *config) error {
	if raw := os.Getenv("IDENTITY_KEY_ISSUE_LIMIT"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return fmt.Errorf("IDENTITY_KEY_ISSUE_LIMIT must be a non-negative integer: %q", raw)
		}
		cfg.keyIssueLimit = n
	}
	if raw := os.Getenv("IDENTITY_ARGON_CONCURRENCY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return fmt.Errorf("IDENTITY_ARGON_CONCURRENCY must be a positive integer: %q", raw)
		}
		cfg.argonConcurrency = n
	}
	return nil
}

// loadEngineConfig fills the engine-provisioning slice (identity slice
// C). Everything is optional: without IDENTITY_ENGINE_URL identity runs
// key-only issuance and the gateway falls back per the feed contract.
// With it, the master key is mandatory — fail fast rather than 502ing
// every issuance — and the URL must be able to carry that key safely:
// HTTPS anywhere, or plain HTTP only on loopback (a local dev engine
// never crosses a wire; CWE-319).
func loadEngineConfig(cfg *config) error {
	cfg.engineURL = os.Getenv("IDENTITY_ENGINE_URL")
	cfg.engineKey = os.Getenv("IDENTITY_ENGINE_KEY")
	cfg.engineName = os.Getenv("IDENTITY_ENGINE_NAME")
	if cfg.engineName == "" {
		cfg.engineName = defaultEngineName
	}
	if cfg.engineURL == "" {
		return nil
	}
	return validateEngineConfig(cfg)
}

func validateEngineConfig(cfg *config) error {
	if cfg.engineKey == "" {
		return errors.New("missing required config: IDENTITY_ENGINE_KEY (set when IDENTITY_ENGINE_URL is set)")
	}
	if !engineURLIsSecure(cfg.engineURL) {
		return fmt.Errorf("IDENTITY_ENGINE_URL must be https:// (or http:// on loopback), got %q", cfg.engineURL)
	}
	return nil
}

// engineURLIsSecure reports whether an engine admin URL may carry the
// master key: HTTPS anywhere, or plain HTTP only on loopback — a local
// dev engine — where no network crossing exists for a passive observer.
// The URL must be a bare origin plus optional base path: a query or
// fragment would break the client's path-appended endpoints, and the
// admin channel has no use for either. The fragment check scans the raw
// string because url.Parse drops a trailing empty "#".
func engineURLIsSecure(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "#") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loadKeyConfig fills the key-capability slice of the config: the
// reveal master key (required, fail fast — the reveal path is part of
// the component's contract) and the issuance rate-limit knobs.
func loadKeyConfig(cfg *config) error {
	masterKey, err := loadMasterKey()
	if err != nil {
		return err
	}
	cfg.keyMasterKey = masterKey
	if cfg.keyIssueWindow, err = envDuration("IDENTITY_KEY_ISSUE_WINDOW", cfg.keyIssueWindow); err != nil {
		return err
	}
	// The issue limit itself is parsed once in loadRuntimeLimits; only
	// the window pairs here.
	return nil
}

// envDuration parses an optional duration env var, defaulting when
// unset.
func envDuration(name string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration like 24h: %w", name, err)
	}
	return d, nil
}

// loadMasterKey decodes IDENTITY_KEY_MASTER_KEY (base64, 32 bytes).
// The reveal path is part of this component's contract, so a missing
// or wrong-sized key is a boot-time misconfiguration, not a runtime
// surprise (fail fast).
func loadMasterKey() ([]byte, error) {
	raw := os.Getenv("IDENTITY_KEY_MASTER_KEY")
	if raw == "" {
		return nil, errors.New("missing required config: IDENTITY_KEY_MASTER_KEY")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("IDENTITY_KEY_MASTER_KEY must be base64: %w", err)
	}
	if len(key) != keyadapter.KeyByteLen {
		return nil, fmt.Errorf("IDENTITY_KEY_MASTER_KEY must decode to %d bytes, got %d", keyadapter.KeyByteLen, len(key))
	}
	return key, nil
}

// provisionBundle carries the slice-C wiring the key, quota, and
// provision capabilities share. A nil provisioner means no engine is
// configured: issuance stays key-only, the map stays empty, and the
// feed omits engine_credential (feedEngineName empty — leftover
// identity_map rows from a since-removed engine must not project).
type provisionBundle struct {
	provisioner    provisionport.EngineProvisioner
	quotaRepo      *quotaadapter.Repository
	mapRepo        *provisionadapter.MapRepository
	feedEngineName string
}

// wireProvision builds the bundle. The LiteLLM client gets a bounded
// HTTP client (AD-25: no globals in production wiring) whose redirect
// policy refuses any hop that may not carry the master key (CWE-319);
// an engine without a master key or with an unsafe URL is a boot-time
// misconfiguration, caught in loadEngineConfig.
// loadAuthnConfig reads the AuthN mode (identity-l1 §5): builtin
// local credentials, or OIDC against a Keycloak-compatible IdP.
func loadAuthnConfig(cfg *config) error {
	cfg.authnMode = os.Getenv("IDENTITY_AUTHN_MODE")
	if cfg.authnMode == "" {
		cfg.authnMode = "builtin"
	}
	switch cfg.authnMode {
	case "builtin":
		return nil
	case "oidc":
		cfg.oidcIssuer = os.Getenv("IDENTITY_OIDC_ISSUER")
		cfg.oidcClientID = os.Getenv("IDENTITY_OIDC_CLIENT_ID")
		cfg.oidcUsernameClaim = os.Getenv("IDENTITY_OIDC_USERNAME_CLAIM")
		cfg.oidcInsecure = os.Getenv("IDENTITY_OIDC_INSECURE") == "1"
		// Opt-in, not opt-out (CodeRabbit security review on PR #141):
		// a valid first-sight IdP account must not become an active
		// local member unless the deployment explicitly says so.
		switch strings.ToLower(os.Getenv("IDENTITY_OIDC_AUTO_REGISTER")) {
		case "1", "true", "yes":
			cfg.oidcAutoRegister = true
		}
		if cfg.oidcIssuer == "" || cfg.oidcClientID == "" {
			return errors.New("missing required config: IDENTITY_OIDC_ISSUER and IDENTITY_OIDC_CLIENT_ID (required when IDENTITY_AUTHN_MODE=oidc)")
		}
		return nil
	default:
		return fmt.Errorf("invalid IDENTITY_AUTHN_MODE %q (want builtin or oidc)", cfg.authnMode)
	}
}

// wireProvider selects the AuthNProvider implementation by mode. OIDC
// construction fetches the IdP's well-known configuration, so a bad
// issuer fails the boot, not the first login.
func wireProvider(ctx context.Context, cfg config, db *sql.DB, repo *principaladapter.Repository, clockFn func() time.Time) (authnport.Provider, error) {
	if cfg.authnMode == "oidc" {
		return authnadapter.NewOIDCProvider(ctx, authnadapter.OIDCConfig{
			Issuer:        cfg.oidcIssuer,
			ClientID:      cfg.oidcClientID,
			UsernameClaim: cfg.oidcUsernameClaim,
			AutoRegister:  cfg.oidcAutoRegister,
			Insecure:      cfg.oidcInsecure,
		}, db, cfg.tokenTTL, rand.Reader, clockFn, repo, authnadapter.NewBindingRepository(db))
	}
	return authnadapter.NewLocalProvider(db, cfg.tokenTTL, rand.Reader, clockFn), nil
}

func wireProvision(cfg config, db *sql.DB) provisionBundle {
	bundle := provisionBundle{
		quotaRepo: quotaadapter.NewRepository(db),
		mapRepo:   provisionadapter.NewMapRepository(db),
	}
	if cfg.engineURL != "" {
		bundle.provisioner = litellm.NewClient(cfg.engineURL, cfg.engineKey, engineAdminClient())
		bundle.feedEngineName = cfg.engineName
	}
	return bundle
}

// engineAdminClient bounds one engine admin call (provision, budget,
// delete) so a hung engine cannot park an issuance, and pins the
// redirect policy: the master key rides the Authorization header, so
// every redirect hop must satisfy the same URL safety rule as the
// configured base URL — a same-host or subdomain redirect to http must
// not silently forward the key.
func engineAdminClient() *http.Client {
	return &http.Client{
		Timeout: engineHTTPTimeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if !engineURLIsSecure(req.URL.String()) {
				return fmt.Errorf("identity: refusing engine redirect to non-HTTPS endpoint %q", req.URL.Redacted())
			}
			return nil
		},
	}
}

// run wires and serves; separated from main for the smoke-test shape
// gateway's main follows.
func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("identityd: open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	pingCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if pingErr := db.PingContext(pingCtx); pingErr != nil {
		return fmt.Errorf("identityd: database unreachable: %w", pingErr)
	}
	if migErr := migrations.Up(cfg.databaseURL); migErr != nil {
		return migErr
	}

	clock := time.Now
	repo := principaladapter.NewRepository(db)
	invites := principaladapter.NewInviteRepository(db)
	policyStore := policyadapter.NewStore(db)
	hasher := authnadapter.Argon2idHasher{}
	clockFn := clock

	principalSvc := principalapp.NewService(repo, invites, policyStore, hasher, rand.Reader, clockFn)

	keyRepo := keyadapter.NewRepository(db)
	sealer, sealErr := keyadapter.NewSealer(cfg.keyMasterKey, rand.Reader)
	if sealErr != nil {
		return sealErr
	}
	keySvc := keyapp.NewService(keyRepo, repo, sealer, rand.Reader, clockFn, cfg.keyIssueLimit, cfg.keyIssueWindow)

	policySvc := policyapp.NewService(policyStore, clockFn)
	// AuthN mode (identity-l1 §5): builtin local credentials, or OIDC
	// (Keycloak-compatible) where an IdP ID token swaps for a local
	// session. OIDC wiring fails identityd's boot on misconfiguration
	// — the discovery fetch is part of construction (fail-fast).
	provider, err := wireProvider(ctx, cfg, db, repo, clockFn)
	if err != nil {
		return err
	}
	loginSvc := authnapp.NewLoginService(provider, cfg.tokenTTL, clockFn)

	// Engine provisioning (identity slice C): nil provisioner means no
	// engine is configured — issuance stays key-only, the map stays
	// empty, and the feed omits engine_credential, so the gateway falls
	// back per its contract.
	bundle := wireProvision(cfg, db)
	if bundle.provisioner != nil {
		keySvc.SetEngineProvisioner(bundle.provisioner, bundle.mapRepo, bundle.quotaRepo, cfg.engineName)
	}
	quotaSvc := quotaapp.NewService(bundle.quotaRepo, repo, bundle.mapRepo, bundle.provisioner, clockFn)
	provisionSvc := provisionapp.NewService(bundle.mapRepo)

	// The feed's revision hub is bumped by every mutating capability
	// after a persisted write; the watch endpoint wakes immediately.
	// The store also projects engine credentials: it unseals
	// identity_map rows for the data plane behind the service token.
	feedSvc := gatewayfeedapp.NewService(
		gatewayfeedadapter.NewStore(db, bundle.feedEngineName, sealer),
		gatewayfeedapp.NewHub(), cfg.watchTimeout)
	principalSvc.SetRevisionNotifier(feedSvc.Hub())
	keySvc.SetRevisionNotifier(feedSvc.Hub())

	mux := routeMux(provider, newArgonLimit(cfg.argonConcurrency),
		principalapp.NewHandler(principalSvc),
		policyapp.NewHandler(policySvc),
		authnapp.NewHandler(loginSvc),
		keyapp.NewHandler(keySvc),
		quotaapp.NewHandler(quotaSvc),
		provisionapp.NewHandler(provisionSvc),
		gatewayfeedapp.NewHandler(feedSvc),
		cfg.gatewayToken,
		cfg.bootstrapKey,
	)

	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("identityd listening", "addr", cfg.listen)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("identityd: graceful shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// routeMux assembles the v1 API. Self register/login are open (the
// argon limiter bounds their hashing work); every other self/admin
// route carries the Bearer auth middleware (admin routes additionally
// require the admin role). The bootstrap route is one-shot and
// key-guarded (IDENTITY_BOOTSTRAP_KEY, constant-time). The
// gateway-facing routes are the internal data-plane contract:
// service-token guarded, never user-facing.
func routeMux(provider authnport.Provider, limit *argonLimit, principalH *principalapp.Handler, policyH *policyapp.Handler, authnH *authnapp.Handler, keyH *keyapp.Handler, quotaH *quotaapp.Handler, provisionH *provisionapp.Handler, feedH *gatewayfeedapp.Handler, gatewayToken, bootstrapKey string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/self/register", limit.wrap(http.HandlerFunc(principalH.RegisterSelf)))
	mux.Handle("POST /v1/self/login", limit.wrap(http.HandlerFunc(authnH.Login)))
	mux.Handle("GET /v1/self/me", requireAuth(provider, false, http.HandlerFunc(principalH.Me)))

	mux.Handle("POST /v1/self/keys", requireAuth(provider, false, http.HandlerFunc(keyH.IssueSelf)))
	mux.Handle("GET /v1/self/keys", requireAuth(provider, false, http.HandlerFunc(keyH.ListSelf)))
	mux.Handle("GET /v1/self/keys/{id}", requireAuth(provider, false, http.HandlerFunc(keyH.GetSelf)))
	mux.Handle("POST /v1/self/keys/{id}/reveal", requireAuth(provider, false, http.HandlerFunc(keyH.RevealSelf)))
	mux.Handle("DELETE /v1/self/keys/{id}", requireAuth(provider, false, http.HandlerFunc(keyH.RevokeSelf)))
	mux.Handle("GET /v1/self/quota", requireAuth(provider, false, http.HandlerFunc(quotaH.GetSelf)))

	mux.Handle("POST /v1/admin/principals", requireAuth(provider, true, http.HandlerFunc(principalH.ProvisionAdmin)))
	mux.Handle("GET /v1/admin/principals", requireAuth(provider, true, http.HandlerFunc(principalH.ListAdmin)))
	mux.Handle("GET /v1/admin/principals/{id}", requireAuth(provider, true, http.HandlerFunc(principalH.GetAdmin)))
	mux.Handle("POST /v1/admin/principals/{id}/approve", requireAuth(provider, true, http.HandlerFunc(principalH.ApproveAdmin)))
	mux.Handle("POST /v1/admin/principals/{id}/status", requireAuth(provider, true, http.HandlerFunc(principalH.SetStatusAdmin)))
	mux.Handle("POST /v1/admin/principals/{id}/roles", requireAuth(provider, true, http.HandlerFunc(principalH.SetRolesAdmin)))
	mux.Handle("POST /v1/admin/invites", requireAuth(provider, true, http.HandlerFunc(principalH.CreateInviteAdmin)))
	mux.Handle("GET /v1/admin/principals/{id}/keys", requireAuth(provider, true, http.HandlerFunc(keyH.ListForPrincipalAdmin)))
	mux.Handle("DELETE /v1/admin/keys/{id}", requireAuth(provider, true, http.HandlerFunc(keyH.RevokeAdmin)))
	mux.Handle("PUT /v1/admin/principals/{id}/quota", requireAuth(provider, true, http.HandlerFunc(quotaH.SetAdmin)))
	mux.Handle("GET /v1/admin/principals/{id}/quota", requireAuth(provider, true, http.HandlerFunc(quotaH.GetAdmin)))
	mux.Handle("GET /v1/admin/identitymap", requireAuth(provider, true, http.HandlerFunc(provisionH.InspectAdmin)))
	mux.Handle("GET /v1/admin/policy", requireAuth(provider, true, http.HandlerFunc(policyH.GetAdmin)))
	mux.Handle("PUT /v1/admin/policy", requireAuth(provider, true, http.HandlerFunc(policyH.SetAdmin)))

	mux.Handle("POST /v1/bootstrap/invite", requireBootstrapKey(bootstrapKey, http.HandlerFunc(principalH.CreateBootstrapInvite)))

	mux.Handle("GET /v1/gateway/feed", gatewayfeedapp.RequireServiceToken(gatewayToken, http.HandlerFunc(feedH.Feed)))
	mux.Handle("POST /v1/gateway/keys/validate", gatewayfeedapp.RequireServiceToken(gatewayToken, http.HandlerFunc(feedH.Validate)))
	return mux
}
