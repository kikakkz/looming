// SPDX-License-Identifier: Apache-2.0

// Package pgtest boots the integration-layer postgres (AD-25): one
// testcontainers instance per test, schema migrated, ready for the
// adapter tests. Test-only — never imported by production code.
package pgtest

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kikakkz/looming/platform/go/migrations"
)

// image is overridable for environments where docker hub is
// unreachable (CI uses the default).
var image = func() string {
	if img := os.Getenv("TOPOLOGY_TEST_POSTGRES_IMAGE"); img != "" {
		return img
	}
	return "postgres:16-alpine"
}()

func init() {
	// Ryuk, the testcontainers reaper, pulls its own image from docker
	// hub at every session start; in environments where docker hub is
	// unreachable that pull fails the whole session. Every container
	// started here is terminated by an explicit t.Cleanup, so the
	// reaper is redundant. Disable it unless the environment already
	// stated a preference.
	if _, ok := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !ok {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}

// NewDB starts a postgres container, migrates the schema, and returns
// an open handle. The container dies with the test.
func NewDB(t *testing.T) *sql.DB {
	db, _ := NewDBWithDSN(t)
	return db
}

// NewDBWithDSN is NewDB plus the connection string, for tests that need
// to reach the database again themselves.
func NewDBWithDSN(t *testing.T) (*sql.DB, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ctr, err := postgres.Run(ctx, image,
		postgres.WithDatabase("topology"),
		postgres.WithUsername("topology"),
		postgres.WithPassword("topology"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = ctr.Terminate(cleanupCtx)
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("container connection string: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, dsn
}
