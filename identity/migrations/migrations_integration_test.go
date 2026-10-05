// SPDX-License-Identifier: Apache-2.0

//go:build integration

package migrations_test

import (
	"context"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // driver registration
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/kikakkz/looming/identity/migrations"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

func TestUpFailurePaths(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		// Port 1 is the discarded TCP port: always refused, no network wait.
		{"unreachable database", "postgres://identity:identity@127.0.0.1:1/identity?sslmode=disable"},
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

func TestMigration0003DownRestoresPreBootstrapSchema(t *testing.T) {
	db, dsn := pgtest.NewDBWithDSN(t) // boots fully migrated (0003 included)
	ctx := context.Background()

	assertColumns := func(want bool) {
		t.Helper()
		var n int
		err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM information_schema.columns
			  WHERE table_schema = 'public' AND table_name = 'invite_tokens'
			    AND column_name IN ('source', 'email')`).Scan(&n)
		if err != nil {
			t.Fatalf("column presence check: %v", err)
		}
		if (n == 2) != want {
			t.Fatalf("source/email columns present=%v (count %d), want %v", n == 2, n, want)
		}
	}
	assertOneShotIndex := func(want bool) {
		t.Helper()
		var n int
		err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pg_indexes
			  WHERE schemaname = 'public' AND tablename = 'invite_tokens'
			    AND indexname = 'invite_tokens_bootstrap_source_uidx'`).Scan(&n)
		if err != nil {
			t.Fatalf("index presence check: %v", err)
		}
		if (n == 1) != want {
			t.Fatalf("bootstrap one-shot index present=%v (count %d), want %v", n == 1, n, want)
		}
	}
	assertConstraint := func() {
		t.Helper()
		_, err := db.ExecContext(ctx,
			`INSERT INTO invite_tokens (token_hash, created_by, expires_at, source)
			 VALUES ($1, 'test', now(), 'bogus')`, []byte{0x1})
		if err == nil {
			t.Fatal("the source CHECK constraint must reject unknown sources")
		}
	}
	assertDefault := func() {
		t.Helper()
		_, err := db.ExecContext(ctx,
			`INSERT INTO invite_tokens (token_hash, created_by, created_at, expires_at)
			 VALUES ($1, 'test', now(), now())`, []byte{0x2})
		if err != nil {
			t.Fatalf("legacy insert without source must keep working: %v", err)
		}
		var source string
		if err := db.QueryRowContext(ctx,
			`SELECT source FROM invite_tokens WHERE token_hash = $1`, []byte{0x2}).Scan(&source); err != nil {
			t.Fatalf("read defaulted source: %v", err)
		}
		if source != "admin" {
			t.Fatalf("legacy rows must default to source 'admin', got %q", source)
		}
	}

	assertColumns(true)
	assertOneShotIndex(true)
	assertConstraint()
	assertDefault()

	// Roll back to the pre-bootstrap schema (0002): both columns and
	// the one-shot index go away.
	m, err := newTestMigrator(dsn)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Migrate(2); err != nil {
		t.Fatalf("migrate down to 0002: %v", err)
	}
	assertColumns(false)
	assertOneShotIndex(false)

	// Forward again: 0003 re-applies cleanly and its rules hold.
	if err := m.Up(); err != nil {
		t.Fatalf("migrate back up: %v", err)
	}
	assertColumns(true)
	assertOneShotIndex(true)
	assertConstraint()
}

// newTestMigrator builds a migrate instance over the package's own SQL
// files (the test's working directory is the package) so tests can
// exercise paths the production API (up-only) does not expose.
func newTestMigrator(dsn string) (*migrate.Migrate, error) {
	src, err := iofs.New(os.DirFS("."), ".")
	if err != nil {
		return nil, err
	}
	m, err := migrate.NewWithSourceInstance("iofs-test", src, dsn)
	if err != nil {
		return nil, err
	}
	return m, nil
}
