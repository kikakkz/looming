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

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver for the store connections

	"github.com/kikakkz/looming/topology/internal/exec"
	hostadapter "github.com/kikakkz/looming/topology/internal/host/adapter"
	"github.com/kikakkz/looming/topology/internal/render"
	topologyadapter "github.com/kikakkz/looming/topology/internal/topology/adapter"
	"github.com/kikakkz/looming/topology/migrations"
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
		Runner:       runner,
		Project:      render.Project,
		Clock:        time.Now,
		Sleep:        time.Sleep,
		ReadFile:     os.ReadFile,
		OpenStores:   openPostgresStores,
		ProvisionDB:  provisionTopologyDB,
		InvitePoster: postBootstrapInvite,
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

	var exists int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM pg_database WHERE datname = $1`, DatabaseName).Scan(&exists)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, createErr := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", DatabaseName)); createErr != nil {
			return fmt.Errorf("apply: create %s database: %w", DatabaseName, createErr)
		}
	case err != nil:
		return fmt.Errorf("apply: probe %s database: %w", DatabaseName, err)
	}

	if err := migrations.Up(databaseURL); err != nil {
		return fmt.Errorf("apply: migrate %s database: %w", DatabaseName, err)
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
