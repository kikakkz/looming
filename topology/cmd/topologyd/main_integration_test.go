// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver for the seed insert
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/tests/pgtest"
)

// freePort reserves an ephemeral port for the test server.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return fmt.Sprintf("127.0.0.1:%d", l.Addr().(*net.TCPAddr).Port)
}

// waitForServer polls until the server answers or the deadline passes.
func waitForServer(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Post(base+"/v1/join", "application/json", strings.NewReader(`{}`))
		if err == nil {
			_ = resp.Body.Close()
			return // any HTTP answer means the listener is up
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("topologyd did not start listening in time")
}

func TestBootMigratesAndServesJoinRoundTrip(t *testing.T) {
	db, dsn := pgtest.NewDBWithDSN(t)

	t.Setenv(databaseURLEnv, dsn)
	listen := freePort(t)
	t.Setenv(listenEnv, listen)

	// Seed a token row directly: the admin-side mint path is exercised
	// at the unit layer; here we prove the booted service migrated the
	// schema (the insert lands — 0003's created_at column exists) and
	// serves the join contract end to end.
	raw := "integration-token"
	sum := sha256.Sum256([]byte(raw))
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO join_tokens (token_hash, role, created_by, expires_at)
		 VALUES ($1, 'engine', 'integration', now() + interval '1 hour')`, sum[:])
	if err != nil {
		t.Fatalf("seed join token (proves migrations ran): %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	errCh := make(chan error, 1)
	go func() { errCh <- run(runCtx, log) }()

	base := "http://" + listen
	waitForServer(t, base)

	resp, err := http.Post(base+"/v1/join", "application/json", strings.NewReader(
		fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.51","labels":["gpu"]}}`, raw)))
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("join must return 201, got %d: %s", resp.StatusCode, body)
	}
	var joined struct {
		HostID     string `json:"host_id"`
		Credential string `json:"credential"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&joined); err != nil {
		t.Fatalf("decode join response: %v", err)
	}
	if joined.HostID == "" || joined.Credential == "" {
		t.Fatalf("join response incomplete: %+v", joined)
	}

	// Re-join over the wire with the minted credential.
	rejoin, err := http.NewRequest(http.MethodPost, base+"/v1/join/rejoin",
		strings.NewReader(`{"labels":["gpu","ssd"]}`))
	if err != nil {
		t.Fatalf("build rejoin: %v", err)
	}
	rejoin.Header.Set("Authorization", "Host "+joined.HostID+":"+joined.Credential)
	rejoinResp, err := http.DefaultClient.Do(rejoin)
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	defer func() { _ = rejoinResp.Body.Close() }()
	if rejoinResp.StatusCode != http.StatusOK {
		t.Fatalf("rejoin must return 200, got %d", rejoinResp.StatusCode)
	}

	// A wrong credential is a 401, indistinguishable from unknown host.
	bad, err := http.NewRequest(http.MethodPost, base+"/v1/join/rejoin", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build bad rejoin: %v", err)
	}
	bad.Header.Set("Authorization", "Host "+joined.HostID+":wrong")
	badResp, err := http.DefaultClient.Do(bad)
	if err != nil {
		t.Fatalf("bad rejoin: %v", err)
	}
	defer func() { _ = badResp.Body.Close() }()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong credential must return 401, got %d", badResp.StatusCode)
	}

	// Cancelling the parent context drives the graceful shutdown path.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run must exit cleanly on context cancel: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not exit after context cancellation")
	}
}

// Compile-time guard: the seed path needs the sql handle shape.
var _ = sql.Open

// TestRunFailsFastOnUnreachableDatabase proves the boot's fail-fast
// contract: a dead database URL is a startup error, not a retry loop.
func TestRunFailsFastOnUnreachableDatabase(t *testing.T) {
	// Port 1 is the discarded TCP port: always refused.
	t.Setenv(databaseURLEnv, "postgres://topology:topology@127.0.0.1:1/topology?sslmode=disable")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := run(context.Background(), log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database unreachable")
}

// TestRunSurfacesListenFailures: an occupied listen address surfaces
// from run (the server goroutine's error wins the select) instead of
// hanging or being swallowed.
func TestRunSurfacesListenFailures(t *testing.T) {
	_, dsn := pgtest.NewDBWithDSN(t)
	t.Setenv(databaseURLEnv, dsn)

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = occupied.Close() }()
	t.Setenv(listenEnv, occupied.Addr().String())

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runErr := run(context.Background(), log)
	require.Error(t, runErr)
	assert.NotErrorIs(t, runErr, http.ErrServerClosed)
}
