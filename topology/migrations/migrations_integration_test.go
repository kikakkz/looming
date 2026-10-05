// SPDX-License-Identifier: Apache-2.0

//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // driver registration
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/kikakkz/looming/topology/migrations"
	"github.com/kikakkz/looming/topology/tests/pgtest"
)

func TestUpFailurePaths(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		// Port 1 is the discarded TCP port: always refused, no network wait.
		{"unreachable database", "postgres://topology:topology@127.0.0.1:1/topology?sslmode=disable"},
		// An unregistered scheme fails instance construction, before any dial.
		{"unknown driver", "unknownscheme://nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := migrations.Up(tc.url); err == nil {
				t.Fatal("up must fail for an unusable database URL")
			}
		})
	}
}

func TestUpIsIdempotentAcrossRestarts(t *testing.T) {
	// pgtest migrates at boot; a second Up against the same database
	// must be a clean no-op so process restarts stay safe.
	_, dsn := pgtest.NewDBWithDSN(t)
	if err := migrations.Up(dsn); err != nil {
		t.Fatalf("second Up must be a no-op, got: %v", err)
	}
}

func TestUpFailsOnDirtyMigrationState(t *testing.T) {
	// Marking the current version dirty (an interrupted migration) must
	// surface from Up, not be silently papered over.
	db, dsn := pgtest.NewDBWithDSN(t)
	if _, err := db.ExecContext(context.Background(),
		`UPDATE schema_migrations SET dirty = true`); err != nil {
		t.Fatalf("mark schema dirty: %v", err)
	}
	if err := migrations.Up(dsn); err == nil {
		t.Fatal("up against a dirty migration state must fail")
	}
}

var phase1Tables = []string{"topology", "hosts", "placements", "join_tokens", "guide", "render_artifacts"}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var found sql.NullString
	err := db.QueryRowContext(context.Background(),
		`SELECT to_regclass($1)`, name).Scan(&found)
	if err != nil {
		t.Fatalf("to_regclass(%q): %v", name, err)
	}
	return found.Valid
}

func TestDownCleansThePhase1Schema(t *testing.T) {
	db, dsn := pgtest.NewDBWithDSN(t)
	for _, table := range phase1Tables {
		if !tableExists(t, db, table) {
			t.Fatalf("table %q must exist after Up", table)
		}
	}

	src, err := iofs.New(os.DirFS("."), ".")
	if err != nil {
		t.Fatalf("migrations source: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		t.Fatalf("migrate init: %v", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Down(); err != nil {
		t.Fatalf("down: %v", err)
	}

	for _, table := range phase1Tables {
		if tableExists(t, db, table) {
			t.Fatalf("table %q must be gone after Down", table)
		}
	}
}
