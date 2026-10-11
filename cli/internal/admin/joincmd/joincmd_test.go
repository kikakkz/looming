// SPDX-License-Identifier: Apache-2.0

package joincmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
)

// fakeJoinServer serves the join contract over httptest and records
// the request payloads.
type fakeJoinServer struct {
	server *httptest.Server
	body   map[string]any
}

func newFakeJoinServer(t *testing.T, status int, payload string) *fakeJoinServer {
	t.Helper()
	fj := &fakeJoinServer{}
	fj.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/join", r.URL.Path)
		fj.body = map[string]any{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&fj.body))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(fj.server.Close)
	return fj
}

func runJoin(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := New(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

func withFakeDial(t *testing.T, addr string) {
	t.Helper()
	orig := dialLocalAddr
	dialLocalAddr = func(string) (string, error) { return addr, nil }
	t.Cleanup(func() { dialLocalAddr = orig })
}

// withFakeFacts pins the machine-facts block the command observes
// (AD-25: no /proc, statfs, or network in the unit layer) and restores
// the production collector afterwards.
func withFakeFacts(t *testing.T, caps *hostdomain.Capabilities) {
	t.Helper()
	orig := collectFacts
	collectFacts = func(context.Context) *hostdomain.Capabilities { return caps }
	t.Cleanup(func() { collectFacts = orig })
}

func TestJoinHappyPathWritesCredentialFile(t *testing.T) {
	fj := newFakeJoinServer(t, http.StatusCreated,
		`{"host_id":"host-0123abcd","credential":"the-credential","cluster":{"access":"http://10.0.0.10:8080"}}`)
	withFakeDial(t, "10.0.0.21")
	withFakeFacts(t, nil)

	credFile := filepath.Join(t.TempDir(), "host.cred")
	out, err := runJoin(t, fj.server.URL, "--token", "tok", "--cred-file", credFile,
		"--label", "gpu", "--label", "ssd")
	require.NoError(t, err)
	assert.Contains(t, out, "joined as host-0123abcd (address 10.0.0.21)")
	assert.Contains(t, out, "cluster access: http://10.0.0.10:8080")

	host := fj.body["host"].(map[string]any)
	assert.Equal(t, "10.0.0.21", host["address"])
	assert.Equal(t, []any{"gpu", "ssd"}, host["labels"])
	assert.Equal(t, "tok", fj.body["token"])

	info, err := os.Stat(credFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the credential file must be 0600")
	//nolint:gosec // the test reads the credential file it just wrote in its own temp dir.
	content, err := os.ReadFile(credFile)
	require.NoError(t, err)
	assert.Equal(t, "host-0123abcd:the-credential\n", string(content))
}

func TestJoinUsesExplicitAddressWithoutDialing(t *testing.T) {
	fj := newFakeJoinServer(t, http.StatusCreated,
		`{"host_id":"host-0123abcd","credential":"cred","cluster":{"access":""}}`)
	// No dial override: an accidental dial would hit the fake server's
	// address and pass, so the assertion is on the submitted address.
	withFakeFacts(t, nil)
	credFile := filepath.Join(t.TempDir(), "host.cred")
	_, err := runJoin(t, fj.server.URL, "--token", "tok", "--address", "192.168.1.5", "--cred-file", credFile)
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.5", fj.body["host"].(map[string]any)["address"])
}

func TestJoinRefusesToOverwriteExistingCredentialFile(t *testing.T) {
	credFile := filepath.Join(t.TempDir(), "host.cred")
	require.NoError(t, os.WriteFile(credFile, []byte("host-old:secret"), 0o600))

	_, err := runJoin(t, "http://10.0.0.11:8081", "--token", "tok", "--cred-file", credFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to overwrite")
}

func TestJoinRotateRefusesWithMessage(t *testing.T) {
	credFile := filepath.Join(t.TempDir(), "host.cred")
	_, err := runJoin(t, "http://10.0.0.11:8081", "--token", "tok", "--cred-file", credFile, "--rotate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported yet")
	assert.Contains(t, err.Error(), "out of scope")
}

func TestJoinServerErrorSurfacesCode(t *testing.T) {
	fj := newFakeJoinServer(t, http.StatusForbidden,
		`{"error":{"code":"token_expired","message":"expired"}}`)
	withFakeDial(t, "10.0.0.21")
	withFakeFacts(t, nil)
	credFile := filepath.Join(t.TempDir(), "host.cred")

	_, err := runJoin(t, fj.server.URL, "--token", "tok", "--cred-file", credFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token_expired")

	_, statErr := os.Stat(credFile)
	assert.True(t, os.IsNotExist(statErr), "a failed join must not leave a credential file")
}

func TestJoinMissingParentDirFailsBeforeSpendingTheToken(t *testing.T) {
	fj := newFakeJoinServer(t, http.StatusCreated,
		`{"host_id":"host-0123abcd","credential":"cred","cluster":{"access":""}}`)
	withFakeDial(t, "10.0.0.21")
	withFakeFacts(t, nil)
	credFile := filepath.Join(t.TempDir(), "no-such-dir", "host.cred")

	_, err := runJoin(t, fj.server.URL, "--token", "tok", "--cred-file", credFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create credential file")
	assert.Nil(t, fj.body, "the one-time token must not be spent when the credential file cannot be reserved")
}

func TestJoinRequiresToken(t *testing.T) {
	_, err := runJoin(t, "http://10.0.0.11:8081")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--token")
}

func TestJoinHelpListsFlags(t *testing.T) {
	var out bytes.Buffer
	cmd := New(&out)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	usage := cmd.Flags().FlagUsages()
	for _, want := range []string{"--token", "--cred-file", "--rotate", "--address", "--host-id", "--label"} {
		assert.Contains(t, usage, want)
	}
}

// TestJoinSendsObservedFacts is the slice-1.3 command-level path: the
// collected block serializes into the join payload and the summary
// line announces exactly what registered.
func TestJoinSendsObservedFacts(t *testing.T) {
	fj := newFakeJoinServer(t, http.StatusCreated,
		`{"host_id":"host-0123abcd","credential":"cred","cluster":{"access":""}}`)
	withFakeDial(t, "10.0.0.21")
	egress := true
	withFakeFacts(t, &hostdomain.Capabilities{
		Hardware:    hostdomain.HardwareCapabilities{CPUCores: 8, MemoryMB: 32768, DiskGB: 457, Arch: "x86_64"},
		Network:     hostdomain.NetworkCapabilities{Egress: &egress},
		CollectedAt: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC),
	})
	credFile := filepath.Join(t.TempDir(), "host.cred")

	out, err := runJoin(t, fj.server.URL, "--token", "tok", "--cred-file", credFile)
	require.NoError(t, err)
	assert.Contains(t, out, "observed facts: cpu_cores=8 memory_mb=32768 disk_gb=457 arch=x86_64 egress=true")

	caps, ok := fj.body["host"].(map[string]any)["capabilities"].(map[string]any)
	require.True(t, ok, "the payload must carry the facts block: %v", fj.body)
	hw := caps["hardware"].(map[string]any)
	assert.Equal(t, float64(8), hw["cpu_cores"])
	assert.Equal(t, float64(32768), hw["memory_mb"])
	assert.Equal(t, float64(457), hw["disk_gb"])
	assert.Equal(t, "x86_64", hw["arch"])
	assert.Equal(t, true, caps["network"].(map[string]any)["egress"])
	assert.Equal(t, "2026-10-11T12:00:00Z", caps["collected_at"])
}

// TestJoinWithoutFactsOmitsTheKey pins the mixed-version wire shape: no
// observed facts, no capabilities key — an old topologyd accepts the
// payload unchanged.
func TestJoinWithoutFactsOmitsTheKey(t *testing.T) {
	fj := newFakeJoinServer(t, http.StatusCreated,
		`{"host_id":"host-0123abcd","credential":"cred","cluster":{"access":""}}`)
	withFakeDial(t, "10.0.0.21")
	withFakeFacts(t, nil)
	credFile := filepath.Join(t.TempDir(), "host.cred")

	out, err := runJoin(t, fj.server.URL, "--token", "tok", "--cred-file", credFile)
	require.NoError(t, err)
	assert.Contains(t, out, "observed facts: none observed (capabilities stay hand-declared)")
	_, present := fj.body["host"].(map[string]any)["capabilities"]
	assert.False(t, present, "absent facts must omit the key, not send null: %v", fj.body)
}
