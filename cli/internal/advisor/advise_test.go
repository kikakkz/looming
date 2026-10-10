// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"bytes"
	"path/filepath"
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
