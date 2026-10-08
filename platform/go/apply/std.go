// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver for the store connections

	"github.com/kikakkz/looming/platform/go/exec"
	guideadapter "github.com/kikakkz/looming/platform/go/guideadapter"
	hostadapter "github.com/kikakkz/looming/platform/go/hostadapter"
	"github.com/kikakkz/looming/platform/go/migrations"
	"github.com/kikakkz/looming/platform/go/render"
	topologyadapter "github.com/kikakkz/looming/platform/go/topologyadapter"
)

const (
	// inviteMaxBodyBytes caps the invite response read.
	inviteMaxBodyBytes = 1 << 20
)

// inviteRequestTimeout bounds the bootstrap-invite HTTP call: a wedged
// identityd must not stall the converge report. A var so tests can
// shorten it (AD-25: no wall-clock assumptions).
var inviteRequestTimeout = 15 * time.Second

// This file is the composition root's production wiring: the real
// store construction and database provisioning behind the Deps seams.
// It carries no unit tests by design (AD-25 layering) — the
// integration layer exercises it for real (see state_integration_test
// and the adapter integration tests); it is excluded from the unit
// coverage profile like the driven adapters.

// StdDeps returns Deps with every seam wired to its production
// implementation, parametrized only by the command runner.
func StdDeps(runner exec.Runner) Deps {
	return Deps{
		Runner:            runner,
		Project:           render.Project,
		Clock:             time.Now,
		Sleep:             time.Sleep,
		ReadFile:          os.ReadFile,
		OpenStores:        openPostgresStores,
		ProvisionDB:       provisionTopologyDB,
		EnsureComponentDB: ensureComponentDatabase,
		InvitePoster:      postBootstrapInvite,
		IdentitydReady:    probeIdentitydReady,
	}
}

// openPostgresStores connects to the topology database and constructs
// the postgres adapters — the composition root's store wiring.
func openPostgresStores(ctx context.Context, databaseURL string) (Stores, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return Stores{}, fmt.Errorf("apply: open topology store: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return Stores{}, fmt.Errorf("apply: ping topology store: %w", err)
	}
	return Stores{
		Topology:  topologyadapter.NewStore(db),
		Registry:  hostadapter.NewRegistry(db),
		Artifacts: topologyadapter.NewArtifactStore(db),
		Guides:    guideadapter.NewStore(db),
	}, nil
}

// provisionTopologyDB creates the topology database over the
// maintenance connection when it does not exist yet, then applies the
// embedded migrations. Both steps are idempotent, so every apply may
// call it: second and later applies find the database and the schema
// migrated and change nothing.
func provisionTopologyDB(ctx context.Context, adminURL, databaseURL string) error {
	db, err := sql.Open("pgx", adminURL)
	if err != nil {
		return fmt.Errorf("apply: open maintenance connection: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := createDatabaseIfMissing(ctx, db, DatabaseName); err != nil {
		return err
	}

	if err := migrations.Up(databaseURL); err != nil {
		return fmt.Errorf("apply: migrate %s database: %w", DatabaseName, err)
	}
	return nil
}

// ensureComponentDatabase creates one declared component's database
// when missing — create-only: unlike the topology database, each
// component owns its schema and runs its own migrations at boot
// (identityd does), so there is nothing here to migrate. The state
// plane calls it BEFORE the converge step starts the containers
// (#130), over the same maintenance connection and catalog-guarded
// CREATE pattern as the topology database.
func ensureComponentDatabase(ctx context.Context, adminURL, database string) error {
	db, err := sql.Open("pgx", adminURL)
	if err != nil {
		return fmt.Errorf("apply: open maintenance connection: %w", err)
	}
	defer func() { _ = db.Close() }()
	return createDatabaseIfMissing(ctx, db, database)
}

// createDatabaseIfMissing creates the named database over an open
// maintenance connection when the catalog does not list it yet — the
// idempotent CREATE DATABASE both the topology database and every
// declared component database go through (#130). An existing database
// is a guarded no-op, so every apply may run it. The name is a fixed
// internal vocabulary today, and quoting through pgx.Identifier keeps
// it that way: the created name can never drift from the probed one.
func createDatabaseIfMissing(ctx context.Context, db *sql.DB, database string) error {
	var exists int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM pg_database WHERE datname = $1`, database).Scan(&exists)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, createErr := db.ExecContext(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); createErr != nil {
			return fmt.Errorf("apply: create %s database: %w", database, createErr)
		}
	case err != nil:
		return fmt.Errorf("apply: probe %s database: %w", database, err)
	}
	return nil
}

// postBootstrapInvite is the production InvitePoster: POST
// identityd's one-shot bootstrap-invite endpoint with the Bootstrap
// scheme key read from the placement's env_file. The 15-second client
// timeout bounds a wedged identityd without stalling the converge
// report.
func postBootstrapInvite(ctx context.Context, endpoint, key, email string) (int, []byte, error) {
	payload, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		return 0, nil, fmt.Errorf("apply: encode bootstrap invite request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/bootstrap/invite", bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("apply: build bootstrap invite request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bootstrap "+key)

	client := &http.Client{Timeout: inviteRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("apply: bootstrap invite request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, inviteMaxBodyBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("apply: read bootstrap invite response: %w", err)
	}
	return resp.StatusCode, body, nil
}

// identitydReadyTimeout bounds ONE readiness probe: a hung connection
// must consume a poll slot, not the whole ~60s budget.
var identitydReadyTimeout = 2 * time.Second

// probeIdentitydReady is the production IdentitydReady probe: GET the
// identityd base URL and report whether ANY HTTP answer arrives. A 4xx
// on a path identityd does not route still proves the server is
// serving — that is the whole point of the readiness gate (#131) —
// while a refused dial or a timeout reports not-yet. The probe never
// demands 2xx: the invite POST itself may legitimately answer 409.
func probeIdentitydReady(ctx context.Context, endpoint string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: identitydReadyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, inviteMaxBodyBytes))
	return true
}
