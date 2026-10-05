// SPDX-License-Identifier: Apache-2.0

//go:build integration

package apply

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/config"
	"github.com/kikakkz/looming/topology/internal/exec"
	"github.com/kikakkz/looming/topology/internal/render"
)

// countingRunner delegates to the real LocalRunner and counts compose
// up invocations, so the idempotency assertion is behavioral (no
// second `up -d`), not temporal.
type countingRunner struct {
	inner exec.Runner
	ups   int
}

func (c *countingRunner) Run(ctx context.Context, name string, args []string, stdin []byte, env []string) ([]byte, error) {
	isUp := false
	for i, a := range args {
		if a == "up" && i+1 < len(args) && args[i+1] == "-d" {
			isUp = true
		}
	}
	if isUp {
		c.ups++
	}
	return c.inner.Run(ctx, name, args, stdin, env)
}

func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = lis.Close() }()
	return lis.Addr().(*net.TCPAddr).Port
}

func itProject(t *testing.T) string {
	t.Helper()
	var b [4]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err)
	return "looming-it-" + hex.EncodeToString(b[:])
}

// TestEnsureStatePlaneRealDocker is the T1 state-plane integration
// test (AD-25 integration layer): a throwaway compose project on the
// local docker daemon, a throwaway host port and data dir, and the
// real ensure path — compose up, pg_isready wait, topology database
// creation, embedded migrations. The second ensure must skip compose
// up entirely. Cleanup removes only this project's containers; no
// other container on the machine is touched.
func TestEnsureStatePlaneRealDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real docker required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The data dir cannot live in t.TempDir: the container writes as
	// its own uid, and the automatic cleanup cannot remove those files.
	// A private temp dir plus an explicit chmod-container cleanup keeps
	// the machine clean.
	dir, err := os.MkdirTemp("", "looming-state-it-")
	require.NoError(t, err)
	envFile := filepath.Join(dir, "postgres.env")
	require.NoError(t, os.WriteFile(envFile, []byte("POSTGRES_PASSWORD=it-secret\n"), 0o600))
	sp := config.StatePostgres{
		Image:   "postgres:16-alpine",
		EnvFile: envFile,
		DataDir: filepath.Join(dir, "postgres"),
		Port:    freePort(t),
	}
	project := itProject(t)

	stateCompose, err := render.StateCompose(render.StatePostgres{
		Image:   sp.Image,
		EnvFile: sp.EnvFile,
		DataDir: sp.DataDir,
		Port:    sp.Port,
	})
	require.NoError(t, err)

	runner := &countingRunner{inner: exec.LocalRunner{}}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()
		_, _ = runner.Run(cleanupCtx, "docker",
			[]string{"compose", "-p", project, "-f", "-", "down", "-v"},
			[]byte(stateCompose), nil)
		// The container wrote its data dir as its own uid; re-chmod it
		// through the cached image so the host can remove it.
		_, _ = runner.Run(cleanupCtx, "docker",
			[]string{"run", "--rm", "-v", dir + ":/mnt", "--entrypoint", "chmod",
				sp.Image, "-R", "a+rwX", "/mnt"}, nil, nil)
		_ = os.RemoveAll(dir)
	})

	pipeline := NewPipeline(Deps{
		Runner:      runner,
		Project:     project,
		Clock:       time.Now,
		Sleep:       time.Sleep,
		ReadFile:    os.ReadFile,
		OpenStores:  openPostgresStores,
		ProvisionDB: provisionTopologyDB,
	})

	databaseURL, err := pipeline.ensureStatePlane(ctx, sp)
	require.NoError(t, err)
	assert.Equal(t, 1, runner.ups, "first boot brings the container up exactly once")
	assert.Contains(t, databaseURL, "/topology")

	// The topology database exists and carries the migrated schema,
	// T0 and T1 tables alike.
	db, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	pingCtx, pingCancel := context.WithTimeout(ctx, 30*time.Second)
	defer pingCancel()
	require.NoError(t, db.PingContext(pingCtx))
	for _, table := range []string{"topology", "hosts", "placements", "render_artifacts"} {
		var found sql.NullString
		require.NoError(t, db.QueryRowContext(pingCtx,
			`SELECT to_regclass($1)`, table).Scan(&found))
		require.True(t, found.Valid, "table %q must exist after ensure", table)
	}

	// The database is real: writes survive the (guarded, no-op)
	// re-provision the second ensure runs.
	_, err = db.ExecContext(pingCtx,
		`INSERT INTO topology (id, access_mode, access_transport, access_endpoint)
		 VALUES ('singleton', 'public', 'direct', 'ip')`)
	require.NoError(t, err)

	_, err = pipeline.ensureStatePlane(ctx, sp)
	require.NoError(t, err)
	assert.Equal(t, 1, runner.ups, "second ensure finds the container healthy and skips compose up")

	var revision int64
	require.NoError(t, db.QueryRowContext(pingCtx,
		`SELECT revision FROM topology WHERE id = 'singleton'`).Scan(&revision))
	assert.Equal(t, int64(1), revision, "the existing database is untouched by re-ensure")
}
