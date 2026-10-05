// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/exec"
)

// TestStdDepsWiresEverySeam pins the composition root's contract: a
// missing InvitePoster regression fails here, not at apply time.
func TestStdDepsWiresEverySeam(t *testing.T) {
	deps := StdDeps(exec.LocalRunner{})
	assert.NotNil(t, deps.Runner)
	assert.NotNil(t, deps.Clock)
	assert.NotNil(t, deps.Sleep)
	assert.NotNil(t, deps.ReadFile)
	assert.NotNil(t, deps.OpenStores)
	assert.NotNil(t, deps.ProvisionDB)
	assert.NotNil(t, deps.InvitePoster, "T2: the bootstrap invite poster must be wired")
	assert.Equal(t, "looming", deps.Project)
}

// TestPostBootstrapInviteRoundTrip exercises the production invite
// poster against an httptest identityd: the 201 body passes through,
// the status is reported for the step's switch, and transport errors
// stay distinguishable from server envelopes.
func TestPostBootstrapInviteRoundTrip(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/bootstrap/invite", r.URL.Path)
		assert.Equal(t, "Bootstrap topsecret", r.Header.Get("Authorization"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "admin@example.com", body["email"])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"tok","expires_at":"2026-10-07T00:00:00Z","invite_url_path":"/v1/self/register"}`))
	}))
	defer server.Close()

	status, body, err := postBootstrapInvite(ctx, server.URL, "topsecret", "admin@example.com")
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, status)
	assert.Contains(t, string(body), `"token":"tok"`)

	// The status is reported, not interpreted: a 409 passes through
	// for the pipeline's step to classify.
	conflict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer conflict.Close()
	status, _, err = postBootstrapInvite(ctx, conflict.URL, "k", "e")
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, status)

	// Transport failures carry no status.
	_, _, err = postBootstrapInvite(ctx, "http://127.0.0.1:1", "k", "e")
	require.Error(t, err)
}

// TestPostBootstrapInviteTimeoutBoundsAWedgedIdentityd: the client
// timeout keeps a black-holed identityd from stalling the converge.
func TestPostBootstrapInviteTimeoutBoundsAWedgedIdentityd(t *testing.T) {
	orig := inviteRequestTimeout
	inviteRequestTimeout = 100 * time.Millisecond
	t.Cleanup(func() { inviteRequestTimeout = orig })

	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block // never answer
	}))
	defer func() {
		close(block)
		server.Close()
	}()

	start := time.Now()
	_, _, err := postBootstrapInvite(context.Background(), server.URL, "k", "e")
	require.Error(t, err)
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond, "the client timeout must actually engage")
	assert.Less(t, elapsed, 5*time.Second, "the poster must give up on its client timeout, not hang forever")
}
