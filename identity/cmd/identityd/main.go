// SPDX-License-Identifier: Apache-2.0

// Command identityd runs the identity component: it migrates the
// schema, provisions the bootstrap admin on first boot, and serves the
// v1 self/admin HTTP API. Wiring only — every decision lives in the
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
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
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
	bootstrapUsername string
	bootstrapPassword string
	tokenTTL          time.Duration
	argonConcurrency  int
	keyIssueLimit     int
	keyIssueWindow    time.Duration
	keyMasterKey      []byte
	gatewayToken      string
	watchTimeout      time.Duration
}

// defaultArgonConcurrency bounds simultaneous argon2id operations on
// the unauthenticated routes (64 MiB of memory each).
const defaultArgonConcurrency = 16

const (
	defaultKeyIssueLimit  = 10
	defaultKeyIssueWindow = 24 * time.Hour
	defaultWatchTimeout   = 30 * time.Second
)

func loadConfig() (config, error) {
	cfg := config{
		listen:            os.Getenv("IDENTITY_LISTEN"),
		databaseURL:       os.Getenv("IDENTITY_DATABASE_URL"),
		bootstrapUsername: os.Getenv("IDENTITY_BOOTSTRAP_ADMIN_USERNAME"),
		bootstrapPassword: os.Getenv("IDENTITY_BOOTSTRAP_ADMIN_PASSWORD"),
		tokenTTL:          24 * time.Hour,
		argonConcurrency:  defaultArgonConcurrency,
		keyIssueLimit:     defaultKeyIssueLimit,
		keyIssueWindow:    defaultKeyIssueWindow,
		gatewayToken:      os.Getenv("IDENTITY_GATEWAY_TOKEN"),
		watchTimeout:      defaultWatchTimeout,
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
	masterKey, err := loadMasterKey()
	if err != nil {
		return config{}, err
	}
	cfg.keyMasterKey = masterKey
	if cfg.tokenTTL, err = envDuration("IDENTITY_TOKEN_TTL", cfg.tokenTTL); err != nil {
		return config{}, err
	}
	if cfg.keyIssueWindow, err = envDuration("IDENTITY_KEY_ISSUE_WINDOW", cfg.keyIssueWindow); err != nil {
		return config{}, err
	}
	if cfg.watchTimeout, err = envDuration("IDENTITY_WATCH_TIMEOUT", cfg.watchTimeout); err != nil {
		return config{}, err
	}
	if raw := os.Getenv("IDENTITY_KEY_ISSUE_LIMIT"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return config{}, fmt.Errorf("IDENTITY_KEY_ISSUE_LIMIT must be a non-negative integer: %q", raw)
		}
		cfg.keyIssueLimit = n
	}
	if raw := os.Getenv("IDENTITY_ARGON_CONCURRENCY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return config{}, fmt.Errorf("IDENTITY_ARGON_CONCURRENCY must be a positive integer: %q", raw)
		}
		cfg.argonConcurrency = n
	}
	return cfg, nil
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

// run wires and serves; separated from main for the smoke-test shape
// gateway's main follows.
func run(log *slog.Logger) error {
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
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("identityd: database unreachable: %w", err)
	}
	if err := migrations.Up(cfg.databaseURL); err != nil {
		return err
	}

	clock := time.Now
	repo := principaladapter.NewRepository(db)
	invites := principaladapter.NewInviteRepository(db)
	policyStore := policyadapter.NewStore(db)
	hasher := authnadapter.Argon2idHasher{}
	clockFn := clock

	principalSvc := principalapp.NewService(repo, invites, policyStore, hasher, rand.Reader, clockFn)
	if err := bootstrapAdmin(context.Background(), repo, principalSvc, cfg, log); err != nil {
		return err
	}

	keyRepo := keyadapter.NewRepository(db)
	sealer, err := keyadapter.NewSealer(cfg.keyMasterKey, rand.Reader)
	if err != nil {
		return err
	}
	keySvc := keyapp.NewService(keyRepo, repo, sealer, rand.Reader, clockFn, cfg.keyIssueLimit, cfg.keyIssueWindow)

	policySvc := policyapp.NewService(policyStore, clockFn)
	provider := authnadapter.NewLocalProvider(db, cfg.tokenTTL, rand.Reader, clockFn)
	loginSvc := authnapp.NewLoginService(provider, cfg.tokenTTL, clockFn)

	// The feed's revision hub is bumped by every mutating capability
	// after a persisted write; the watch endpoint wakes immediately.
	feedSvc := gatewayfeedapp.NewService(gatewayfeedadapter.NewStore(db), gatewayfeedapp.NewHub(), cfg.watchTimeout)
	principalSvc.SetRevisionNotifier(feedSvc.Hub())
	keySvc.SetRevisionNotifier(feedSvc.Hub())

	mux := routeMux(provider, newArgonLimit(cfg.argonConcurrency),
		principalapp.NewHandler(principalSvc),
		policyapp.NewHandler(policySvc),
		authnapp.NewHandler(loginSvc),
		keyapp.NewHandler(keySvc),
		gatewayfeedapp.NewHandler(feedSvc),
		cfg.gatewayToken,
	)

	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
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
// require the admin role). The gateway-facing routes are the internal
// data-plane contract: service-token guarded, never user-facing.
func routeMux(provider authnport.Provider, limit *argonLimit, principalH *principalapp.Handler, policyH *policyapp.Handler, authnH *authnapp.Handler, keyH *keyapp.Handler, feedH *gatewayfeedapp.Handler, gatewayToken string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/self/register", limit.wrap(http.HandlerFunc(principalH.RegisterSelf)))
	mux.Handle("POST /v1/self/login", limit.wrap(http.HandlerFunc(authnH.Login)))
	mux.Handle("GET /v1/self/me", requireAuth(provider, false, http.HandlerFunc(principalH.Me)))

	mux.Handle("POST /v1/self/keys", requireAuth(provider, false, http.HandlerFunc(keyH.IssueSelf)))
	mux.Handle("GET /v1/self/keys", requireAuth(provider, false, http.HandlerFunc(keyH.ListSelf)))
	mux.Handle("GET /v1/self/keys/{id}", requireAuth(provider, false, http.HandlerFunc(keyH.GetSelf)))
	mux.Handle("POST /v1/self/keys/{id}/reveal", requireAuth(provider, false, http.HandlerFunc(keyH.RevealSelf)))
	mux.Handle("DELETE /v1/self/keys/{id}", requireAuth(provider, false, http.HandlerFunc(keyH.RevokeSelf)))

	mux.Handle("POST /v1/admin/principals", requireAuth(provider, true, http.HandlerFunc(principalH.ProvisionAdmin)))
	mux.Handle("GET /v1/admin/principals", requireAuth(provider, true, http.HandlerFunc(principalH.ListAdmin)))
	mux.Handle("GET /v1/admin/principals/{id}", requireAuth(provider, true, http.HandlerFunc(principalH.GetAdmin)))
	mux.Handle("POST /v1/admin/principals/{id}/approve", requireAuth(provider, true, http.HandlerFunc(principalH.ApproveAdmin)))
	mux.Handle("POST /v1/admin/principals/{id}/status", requireAuth(provider, true, http.HandlerFunc(principalH.SetStatusAdmin)))
	mux.Handle("POST /v1/admin/invites", requireAuth(provider, true, http.HandlerFunc(principalH.CreateInviteAdmin)))
	mux.Handle("GET /v1/admin/principals/{id}/keys", requireAuth(provider, true, http.HandlerFunc(keyH.ListForPrincipalAdmin)))
	mux.Handle("DELETE /v1/admin/keys/{id}", requireAuth(provider, true, http.HandlerFunc(keyH.RevokeAdmin)))
	mux.Handle("GET /v1/admin/policy", requireAuth(provider, true, http.HandlerFunc(policyH.GetAdmin)))
	mux.Handle("PUT /v1/admin/policy", requireAuth(provider, true, http.HandlerFunc(policyH.SetAdmin)))

	mux.Handle("GET /v1/gateway/feed", gatewayfeedapp.RequireServiceToken(gatewayToken, http.HandlerFunc(feedH.Feed)))
	mux.Handle("POST /v1/gateway/keys/validate", gatewayfeedapp.RequireServiceToken(gatewayToken, http.HandlerFunc(feedH.Validate)))
	return mux
}

// bootstrapAdmin provisions the first admin when the principals table
// is empty and is a no-op otherwise — idempotent across restarts. On an
// empty table without configured credentials it fails fast: a
// deployment that never names its first admin is a misconfiguration,
// not an implicit choice.
func bootstrapAdmin(ctx context.Context, repo interface {
	Count(context.Context) (int64, error)
}, svc *principalapp.Service, cfg config, log *slog.Logger) error {
	n, err := repo.Count(ctx)
	if err != nil {
		return fmt.Errorf("identityd: bootstrap check: %w", err)
	}
	if n > 0 {
		return nil
	}
	if cfg.bootstrapUsername == "" || cfg.bootstrapPassword == "" {
		return errors.New("empty principals table requires IDENTITY_BOOTSTRAP_ADMIN_USERNAME and IDENTITY_BOOTSTRAP_ADMIN_PASSWORD")
	}
	if _, err := svc.Provision(ctx, principalapp.ProvisionInput{
		Username: cfg.bootstrapUsername,
		Password: cfg.bootstrapPassword,
		Kind:     principaldomain.KindHuman,
		Roles:    []string{principaldomain.RoleAdmin},
	}); err != nil {
		return fmt.Errorf("identityd: bootstrap admin: %w", err)
	}
	log.Info("bootstrap admin provisioned", "username", cfg.bootstrapUsername)
	return nil
}
