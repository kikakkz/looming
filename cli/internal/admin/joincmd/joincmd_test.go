// SPDX-License-Identifier: Apache-2.0

package joincmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestJoinHappyPathWritesCredentialFile(t *testing.T) {
	fj := newFakeJoinServer(t, http.StatusCreated,
		`{"host_id":"host-0123abcd","credential":"the-credential","cluster":{"access":"http://10.0.0.10:8080"}}`)
	withFakeDial(t, "10.0.0.21")

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
