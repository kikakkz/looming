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

	"github.com/kikakkz/looming/cli/internal/profile"
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
	cmd := newAdvise(&bytes.Buffer{}, &bytes.Buffer{})
	flag := cmd.Flags().Lookup("file")
	require.NotNil(t, flag)
	assert.Equal(t, defaultTopologyPath, flag.DefValue)

	var stdout, stderr bytes.Buffer
	cmd = newAdvise(&stdout, &stderr)
	cmd.SetArgs([]string{"--file", ""})
	err := cmd.Execute()
	require.Error(t, err, "an empty --file is a usage error, not a silent default")
}

// TestAdviseReasonCommandEndToEnd: the cobra surface, end to end — a
// loopback genesis fake serves the recorded response, the operator's
// decision arrives on piped stdin, and the confirm lands the splice.
// This is the coverage home for the production wiring (stdin asker,
// terminal detection, editor runner, credential redaction hook).
func TestAdviseReasonCommandEndToEnd(t *testing.T) {
	server := chatFake(t, `{"placements":[{"component":"identityd","host":"app-1","reason":"LAN reachability"}],"risks":[]}`)

	home := t.TempDir()
	t.Setenv("HOME", home)
	creds, err := profile.LoadCredentials()
	require.NoError(t, err)
	creds.SetGenesis(genesisKeyFixture)
	require.NoError(t, creds.Save())

	path := filepath.Join(home, "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts:
  - id: gw-1
    address: 10.0.0.11
    capabilities:
      hardware: {cpu_cores: 8, memory_mb: 16384, disk_gb: 200, arch: x86_64}
      network: {zone: cloud, egress: true}
  - id: app-1
    address: 10.0.0.12
    capabilities:
      hardware: {cpu_cores: 4, memory_mb: 8192, disk_gb: 100, arch: x86_64}
      network: {zone: lan, egress: true}
placements:
  - component: gateway-front
    host: gw-1
    ports: {http: 8080}
genesis: {endpoint: "`+server.URL+`/v1"}
`), 0o600))

	// The decide prompt reads os.Stdin; pipe the confirm answer.
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, _ = w.WriteString("c\n")
	require.NoError(t, w.Close())
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	var stdout, stderr bytes.Buffer
	cmd := newAdvise(&stdout, &stderr)
	cmd.SetArgs([]string{"--file", path, "--reason", "--preference", "lean edge"})
	require.NoError(t, cmd.Execute())

	assert.Contains(t, stdout.String(), "PROPOSED PLACEMENTS")
	assert.Contains(t, stdout.String(), "never automatic")
	assert.Contains(t, stdout.String(), "+  - component: identityd\n+    host: app-1", "the preview is the unified diff")
	assert.Contains(t, stdout.String(), "ports: {http: 8080}", "an untouched entry renders byte-stable (no diff noise)")
	// The piped-stdin environment skips the interactive preference
	// prompt (--preference carried it) and the genesis sync notes the
	// pre-converge stage without failing the command.
	assert.Contains(t, stderr.String(), "stage 1")

	// #nosec G304 -- the test reads the fixture file it staged.
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(written), "- component: identityd\n    host: app-1")
}

// TestAdviseReasonCommandWithoutChannel: --reason with no genesis
// configured and no gateway switch — the command fails closed with the
// recovery hint, after the sync notes.
func TestAdviseReasonCommandWithoutChannel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`), 0o600))

	var stdout, stderr bytes.Buffer
	cmd := newAdvise(&stdout, &stderr)
	cmd.SetArgs([]string{"--file", path, "--reason"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "genesis set")
}

// TestAdviseReasonFlagContract: the flag surface the command exposes.
func TestAdviseReasonFlagContract(t *testing.T) {
	cmd := newAdvise(&bytes.Buffer{}, &bytes.Buffer{})
	for _, name := range []string{"file", "reason", "preference"} {
		require.NotNil(t, cmd.Flags().Lookup(name), "flag --%s must exist", name)
	}
}

// TestStdinAskerAndTerminalDetection: the asker reads one line and
// trims it; a test process's stdin is not a terminal, so the
// interactive prompt stays out of CI's way.
func TestStdinAskerAndTerminalDetection(t *testing.T) {
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, _ = w.WriteString("  yes please  \n")
	require.NoError(t, w.Close())
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	var stdout bytes.Buffer
	answer, err := stdinAsker(&stdout)("choose: ")
	require.NoError(t, err)
	assert.Equal(t, "yes please", answer)
	assert.Equal(t, "choose: ", stdout.String())
	assert.False(t, stdinIsTerminal())

	if _, err := stdinAsker(&bytes.Buffer{})("again: "); err == nil {
		t.Fatal("an exhausted stdin must surface the read error")
	}
}

// TestEditorRunnerContract: no $EDITOR means no edit function (the
// decide step explains itself); a bogus $EDITOR wires a function that
// surfaces the exec failure.
func TestEditorRunnerContract(t *testing.T) {
	t.Setenv("EDITOR", "")
	if editorRunner() != nil {
		t.Fatal("an unset $EDITOR must leave the edit function nil")
	}

	t.Setenv("EDITOR", "/definitely/not/an/editor")
	edit := editorRunner()
	if edit == nil {
		t.Fatal("a set $EDITOR must wire the edit function")
	}
	if err := edit(filepath.Join(t.TempDir(), "candidate.yaml")); err == nil {
		t.Fatal("a bogus $EDITOR must surface the exec error")
	}
}

// TestCredentialRedactionScrub: every credential the store holds is
// scrubbed from echoed model output; an empty store scrubs nothing.
func TestCredentialRedactionScrub(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	redact := credentialRedaction()
	assert.Equal(t, "clean text", redact("clean text"), "no secrets wired — identity scrub")

	creds, err := profile.LoadCredentials()
	require.NoError(t, err)
	creds.SetGenesis(genesisKeyFixture)
	creds.SetLoomingKey(serviceCredentialName, "lk-service-raw")
	require.NoError(t, creds.Save())

	redact = credentialRedaction()
	assert.Equal(t, "*** and ***", redact(genesisKeyFixture+" and lk-service-raw"))
}
