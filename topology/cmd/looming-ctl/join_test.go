// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	out, err := runWith(t, "join", fj.server.URL, "--token", "tok", "--cred-file", credFile,
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
	_, err := runWith(t, "join", fj.server.URL, "--token", "tok", "--address", "192.168.1.5", "--cred-file", credFile)
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.5", fj.body["host"].(map[string]any)["address"])
}

func TestJoinRefusesToOverwriteExistingCredentialFile(t *testing.T) {
	credFile := filepath.Join(t.TempDir(), "host.cred")
	require.NoError(t, os.WriteFile(credFile, []byte("host-old:secret"), 0o600))

	_, err := runWith(t, "join", "http://10.0.0.11:8081", "--token", "tok", "--cred-file", credFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to overwrite")
}

func TestJoinRotateRefusesWithMessage(t *testing.T) {
	credFile := filepath.Join(t.TempDir(), "host.cred")
	_, err := runWith(t, "join", "http://10.0.0.11:8081", "--token", "tok", "--cred-file", credFile, "--rotate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported yet")
	assert.Contains(t, err.Error(), "out of scope")
}

func TestJoinServerErrorSurfacesCode(t *testing.T) {
	fj := newFakeJoinServer(t, http.StatusForbidden,
		`{"error":{"code":"token_expired","message":"expired"}}`)
	withFakeDial(t, "10.0.0.21")
	credFile := filepath.Join(t.TempDir(), "host.cred")

	_, err := runWith(t, "join", fj.server.URL, "--token", "tok", "--cred-file", credFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token_expired")

	_, statErr := os.Stat(credFile)
	assert.True(t, os.IsNotExist(statErr), "a failed join must not leave a credential file")
}

func TestJoinRequiresToken(t *testing.T) {
	_, err := runWith(t, "join", "http://10.0.0.11:8081")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--token")
}

func TestTokenCreateValidatesRoleBeforeTouchingTheDatabase(t *testing.T) {
	_, err := runWith(t, "token", "create", "--role", "superuser")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--role")

	_, err = runWith(t, "token", "create", "--role", "engine", "--ttl", "not-a-duration")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--ttl")
}

func TestTokenCommandsRequireDatabaseURL(t *testing.T) {
	t.Setenv(databaseURLEnv, "")
	for _, args := range [][]string{
		{"token", "create", "--role", "engine"},
		{"token", "list"},
		{"token", "revoke", "abcd"},
	} {
		_, err := runWith(t, args...)
		require.Error(t, err, "%v", args)
		assert.Contains(t, err.Error(), databaseURLEnv)
	}
}

func TestRootHelpListsT2Commands(t *testing.T) {
	out, err := runWith(t, "--help")
	require.NoError(t, err)
	for _, want := range []string{"apply", "token", "join"} {
		assert.Contains(t, out, want)
	}

	out, err = runWith(t, "token", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "create")
	assert.Contains(t, out, "list")
	assert.Contains(t, out, "revoke")

	out, err = runWith(t, "join", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "--token")
	assert.Contains(t, out, "--cred-file")
	assert.Contains(t, out, "--rotate")
}

func TestApplyPrintInviteFlagRegistered(t *testing.T) {
	out, err := runWith(t, "apply", "--help")
	require.NoError(t, err)
	assert.True(t, strings.Contains(out, "--print-invite"))
}
