// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver for the store connections

	"github.com/kikakkz/looming/topology/internal/exec"
	hostadapter "github.com/kikakkz/looming/topology/internal/host/adapter"
	"github.com/kikakkz/looming/topology/internal/render"
	topologyadapter "github.com/kikakkz/looming/topology/internal/topology/adapter"
	"github.com/kikakkz/looming/topology/migrations"
)

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
		Runner:      runner,
		Project:     render.Project,
		Clock:       time.Now,
		Sleep:       time.Sleep,
		ReadFile:    os.ReadFile,
		OpenStores:  openPostgresStores,
		ProvisionDB: provisionTopologyDB,
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
