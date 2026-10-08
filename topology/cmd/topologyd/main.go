// SPDX-License-Identifier: Apache-2.0

// Command topologyd runs the topology component's long-running service:
// the pull-join endpoints new and existing hosts talk to
// (topology-l1 §7 — POST /v1/join and POST /v1/join/rejoin). It migrates
// the schema on boot and serves until SIGTERM/SIGINT. Wiring only —
// every decision lives in the capability packages (AD-23).
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
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver

	guideadapter "github.com/kikakkz/looming/platform/go/guideadapter"
	guideapp "github.com/kikakkz/looming/platform/go/guideapp"
	hostadapter "github.com/kikakkz/looming/platform/go/hostadapter"
	joinadapter "github.com/kikakkz/looming/platform/go/joinadapter"
	joinapp "github.com/kikakkz/looming/platform/go/joinapp"
	"github.com/kikakkz/looming/platform/go/migrations"
	topologyadapter "github.com/kikakkz/looming/platform/go/topologyadapter"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log) // app-layer error logging lands in the same handler
	if err := run(context.Background(), log); err != nil {
		log.Error("topologyd exited", "err", err)
		os.Exit(1)
	}
}

// config is the environment-driven configuration (TOPOLOGY_ prefix).
type config struct {
	listen       string
	databaseURL  string
	serviceToken string
}

const (
	defaultListen   = ":8081"
	databaseURLEnv  = "TOPOLOGY_DATABASE_URL"
	listenEnv       = "TOPOLOGY_LISTEN"
	serviceTokenEnv = "TOPOLOGY_SERVICE_TOKEN"
	shutdownGrace   = 10 * time.Second
	dbPingTimeout   = 15 * time.Second
)

// loadConfig reads the environment. TOPOLOGY_DATABASE_URL is required —
// a join service without its database is a boot-time misconfiguration,
// not a runtime surprise (fail fast, identityd precedent).
// TOPOLOGY_SERVICE_TOKEN is optional: without it the guide endpoint
// answers 503 instead of serving the snapshot unguarded.
func loadConfig() (config, error) {
	cfg := config{
		listen:       os.Getenv(listenEnv),
		databaseURL:  os.Getenv(databaseURLEnv),
		serviceToken: os.Getenv(serviceTokenEnv),
	}
	if cfg.listen == "" {
		cfg.listen = defaultListen
	}
	if cfg.databaseURL == "" {
		return config{}, fmt.Errorf("missing required config: %s", databaseURLEnv)
	}
	return cfg, nil
}

// run wires and serves; separated from main for the smoke-test shape
// identityd's main follows.
func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("topologyd: open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	pingCtx, cancel := context.WithTimeout(ctx, dbPingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("topologyd: database unreachable: %w", err)
	}
	if err := migrations.Up(cfg.databaseURL); err != nil {
		return err
	}

	svc := joinapp.NewService(
		joinadapter.NewTokenStore(db),
		hostadapter.NewRegistry(db),
		topologyadapter.NewStore(db),
		rand.Reader,
		time.Now,
	)
	guideSvc := guideapp.NewService(
		guideadapter.NewStore(db),
		topologyadapter.NewStore(db),
		hostadapter.NewRegistry(db),
		time.Now,
	)
	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           routeMux(joinapp.NewHandler(svc), guideapp.NewHandler(guideSvc, cfg.serviceToken)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		log.Info("topologyd listening", "addr", cfg.listen)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-serveCtx.Done():
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("topologyd: graceful shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// routeMux assembles the v1 API: the join/rejoin surface and the
// guide's internal read endpoint. The join routes are
// token/credential-guarded by their own payloads; the guide route
// carries its own service-token guard — no session machinery in
// phase 1 (topology-l1 §8 PEP).
func routeMux(joinH *joinapp.Handler, guideH *guideapp.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/join", http.HandlerFunc(joinH.Join))
	mux.Handle("POST /v1/join/rejoin", http.HandlerFunc(joinH.Rejoin))
	mux.Handle("GET /v1/internal/guide", http.HandlerFunc(guideH.Guide))
	return mux
}
