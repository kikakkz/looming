// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunPrintsTableAndExitsZeroEvenWithInfeasiblePairs: the matrix is
// the answer — infeasible pairs still render and the run succeeds.
func TestRunPrintsTableAndExitsZeroEvenWithInfeasiblePairs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	require.NoError(t, run(&stdout, &stderr, filepath.Join("testdata", "topology.yaml")))

	out := stdout.String()
	assert.Contains(t, out, "COMPONENT")
	assert.Contains(t, out, "FEASIBLE")
	assert.Contains(t, out, "INFEASIBLE")
	assert.Contains(t, out, "missing facts:")
	assert.Empty(t, stderr.String())
}

// TestRunWarnsAndSkipsRecordWhenTableWriteFails: a broken stdout
// means the table never reached the operator — warn, exit 0, and
// record nothing.
func TestRunWarnsAndSkipsRecordWhenTableWriteFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stderr strings.Builder
	require.NoError(t, run(failWriter{}, &stderr, filepath.Join("testdata", "topology.yaml")))
	assert.Contains(t, stderr.String(), "table not written")
	entries, err := os.ReadDir(filepath.Join(home, sessionsDirName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		// No directory, no record — exactly the point.
	case err != nil:
		require.NoError(t, err)
	default:
		assert.Empty(t, entries, "no session record when the table was not delivered")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errWrite }

var errWrite = errors.New("write failed")

// TestRunLoadFailureExitsNonZero: an unreadable or invalid topology
// file is the command's only failure mode.
func TestRunLoadFailureExitsNonZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	err := run(&stdout, &stderr, filepath.Join("testdata", "does-not-exist.yaml"))
	require.Error(t, err)
	assert.Empty(t, stdout.String())
}

func TestNewFlagDefaults(t *testing.T) {
	cmd := New(&bytes.Buffer{}, &bytes.Buffer{})
	flag := cmd.Flags().Lookup("file")
	require.NotNil(t, flag)
	assert.Equal(t, defaultTopologyPath, flag.DefValue)

	var stdout, stderr bytes.Buffer
	cmd = New(&stdout, &stderr)
	cmd.SetArgs([]string{"--file", ""})
	err := cmd.Execute()
	require.Error(t, err, "an empty --file is a usage error, not a silent default")
}
