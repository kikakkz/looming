// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
)

func TestAppendSessionRecordCreatesDirAndAppends(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	rec := sessionRecord{
		Timestamp:     "2026-10-09T12:34:56Z",
		File:          "/etc/looming/topology.yaml",
		ProfileSHA256: "abc123",
		FactsSnapshot: []config.Host{{ID: "gw-1", Address: "10.0.0.11"}},
		Matrix:        matrixSummary{Pairs: 3, Feasible: 2, Infeasible: 1},
		Decision:      decisionTableViewed,
	}
	require.NoError(t, appendSessionRecordTo(dir, rec))

	// The directory carries the credentials-class mode (AD-37 §5).
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "session directory must be 0700")

	path := filepath.Join(dir, "2026-10-09T12:34:56Z.jsonl")
	content, err := os.ReadFile(path) // #nosec G304 -- test-controlled path.
	require.NoError(t, err)
	var got sessionRecord
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(content))), &got))
	assert.Equal(t, rec, got)

	// Append-only: a second record lands on a new line, the first
	// survives byte-for-byte.
	rec.Decision = "table-viewed-again"
	require.NoError(t, appendSessionRecordTo(dir, rec))
	content, err = os.ReadFile(path) // #nosec G304 -- test-controlled path.
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], `"abc123"`)
	assert.Contains(t, lines[1], "table-viewed-again")

	fileInfo, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm(), "session file must be 0600")
}

// TestAppendSessionRecordErrorPaths covers the fail-closed branches:
// home resolution, directory creation, and log-open failures each
// surface as errors (run() downgrades them to a warning — the exit
// contract reserves non-zero for load/validation failures).
func TestAppendSessionRecordErrorPaths(t *testing.T) {
	rec := sessionRecord{Timestamp: "2026-10-09T12:34:56Z", Decision: decisionTableViewed}

	home := t.TempDir()
	stub := userHomeDir
	userHomeDir = func() (string, error) { return "", errNoHome }
	t.Cleanup(func() { userHomeDir = stub })
	require.Error(t, appendSessionRecord(rec))
	userHomeDir = stub

	// A regular file where the directory belongs: MkdirAll fails.
	notDir := filepath.Join(home, "not-a-dir")
	require.NoError(t, os.WriteFile(notDir, []byte("x"), 0o600))
	require.Error(t, appendSessionRecordTo(notDir, rec))

	// A directory where the log file belongs: OpenFile fails.
	dir := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, rec.Timestamp+".jsonl"), 0o700))
	require.Error(t, appendSessionRecordTo(dir, rec))
}

var errNoHome = errors.New("no home directory")

// TestRunWarnsButExitsZeroWhenSessionLogFails: the table already
// answered — a session-log failure warns on stderr and still exits 0.
func TestRunWarnsButExitsZeroWhenSessionLogFails(t *testing.T) {
	stub := userHomeDir
	userHomeDir = func() (string, error) { return "", errNoHome }
	t.Cleanup(func() { userHomeDir = stub })

	var stdout, stderr strings.Builder
	require.NoError(t, run(&stdout, &stderr, filepath.Join("testdata", "topology.yaml")))
	assert.Contains(t, stdout.String(), "FEASIBLE")
	assert.Contains(t, stderr.String(), "session record not written")
}

func TestRunAppendsSessionRecordUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr strings.Builder
	require.NoError(t, run(&stdout, &stderr, filepath.Join("testdata", "topology.yaml")))

	entries, err := os.ReadDir(filepath.Join(home, sessionsDirName))
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one session log file")
	content, err := os.ReadFile(filepath.Join(home, sessionsDirName, entries[0].Name())) // #nosec G304 -- test-controlled path.
	require.NoError(t, err)
	var rec sessionRecord
	require.NoError(t, json.Unmarshal(content, &rec))
	assert.Equal(t, decisionTableViewed, rec.Decision)
	assert.Equal(t, matrixSummary{Pairs: 9, Feasible: 4, Infeasible: 5}, rec.Matrix)
	assert.NotEmpty(t, rec.ProfileSHA256)
	assert.Len(t, rec.FactsSnapshot, 3)
	ts, err := time.Parse(time.RFC3339, rec.Timestamp)
	require.NoError(t, err)
	assert.True(t, ts.UTC().Equal(ts), "timestamp must be UTC-rendered RFC3339")
}
