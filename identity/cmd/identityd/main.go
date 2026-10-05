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
}

// defaultArgonConcurrency bounds simultaneous argon2id operations on
// the unauthenticated routes (64 MiB of memory each).
const defaultArgonConcurrency = 16

func loadConfig() (config, error) {
	cfg := config{
		listen:            os.Getenv("IDENTITY_LISTEN"),
		databaseURL:       os.Getenv("IDENTITY_DATABASE_URL"),
		bootstrapUsername: os.Getenv("IDENTITY_BOOTSTRAP_ADMIN_USERNAME"),
		bootstrapPassword: os.Getenv("IDENTITY_BOOTSTRAP_ADMIN_PASSWORD"),
		tokenTTL:          24 * time.Hour,
		argonConcurrency:  defaultArgonConcurrency,
	}
	if cfg.listen == "" {
		cfg.listen = ":8080"
	}
	if cfg.databaseURL == "" {
		return config{}, errors.New("missing required config: IDENTITY_DATABASE_URL")
	}
	if raw := os.Getenv("IDENTITY_TOKEN_TTL"); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return config{}, fmt.Errorf("IDENTITY_TOKEN_TTL must be a duration like 24h: %w", err)
		}
		cfg.tokenTTL = ttl
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

	policySvc := policyapp.NewService(policyStore, clockFn)
	provider := authnadapter.NewLocalProvider(db, cfg.tokenTTL, rand.Reader, clockFn)
	loginSvc := authnapp.NewLoginService(provider, cfg.tokenTTL, clockFn)
	mux := routeMux(provider, newArgonLimit(cfg.argonConcurrency),
		principalapp.NewHandler(principalSvc),
		policyapp.NewHandler(policySvc),
		authnapp.NewHandler(loginSvc),
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
// argon limiter bounds their hashing work); every other route carries
// the Bearer auth middleware, and admin routes additionally require
// the admin role.
func routeMux(provider authnport.Provider, limit *argonLimit, principalH *principalapp.Handler, policyH *policyapp.Handler, authnH *authnapp.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/self/register", limit.wrap(http.HandlerFunc(principalH.RegisterSelf)))
	mux.Handle("POST /v1/self/login", limit.wrap(http.HandlerFunc(authnH.Login)))
	mux.Handle("GET /v1/self/me", requireAuth(provider, false, http.HandlerFunc(principalH.Me)))

	mux.Handle("POST /v1/admin/principals", requireAuth(provider, true, http.HandlerFunc(principalH.ProvisionAdmin)))
	mux.Handle("GET /v1/admin/principals", requireAuth(provider, true, http.HandlerFunc(principalH.ListAdmin)))
	mux.Handle("GET /v1/admin/principals/{id}", requireAuth(provider, true, http.HandlerFunc(principalH.GetAdmin)))
	mux.Handle("POST /v1/admin/principals/{id}/approve", requireAuth(provider, true, http.HandlerFunc(principalH.ApproveAdmin)))
	mux.Handle("POST /v1/admin/principals/{id}/status", requireAuth(provider, true, http.HandlerFunc(principalH.SetStatusAdmin)))
	mux.Handle("POST /v1/admin/invites", requireAuth(provider, true, http.HandlerFunc(principalH.CreateInviteAdmin)))
	mux.Handle("GET /v1/admin/policy", requireAuth(provider, true, http.HandlerFunc(policyH.GetAdmin)))
	mux.Handle("PUT /v1/admin/policy", requireAuth(provider, true, http.HandlerFunc(policyH.SetAdmin)))
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
