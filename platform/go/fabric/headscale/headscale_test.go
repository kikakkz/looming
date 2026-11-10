// SPDX-License-Identifier: Apache-2.0

package headscale_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/fabric/headscale"
)

const realShapeResponse = `{"preAuthKey":{"user":{"id":"1","name":"ops"},"id":"7","key":"tskey-auth-abc123","reusable":true,"ephemeral":false,"used":false,"expiration":"2026-11-17T10:00:00Z","createdAt":"2026-11-10T10:00:00Z","aclTags":[]}}`

type recordedRequest struct {
	method        string
	path          string
	authorization string
	contentType   string
	body          map[string]any
}

func newMockServer(t *testing.T, status int, response string, recorded *recordedRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded.method = r.Method
		recorded.path = r.URL.Path
		recorded.authorization = r.Header.Get("Authorization")
		recorded.contentType = r.Header.Get("Content-Type")
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&recorded.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCreatePreAuthKeySendsVerifiedShape(t *testing.T) {
	var recorded recordedRequest
	srv := newMockServer(t, http.StatusOK, realShapeResponse, &recorded)

	client := headscale.NewClient(srv.URL, "test-api-key")
	before := time.Now()
	key, err := client.CreatePreAuthKey(context.Background(), 1, headscale.PreAuthKeyOpts{
		Reusable:  true,
		Ephemeral: true,
		TTL:       time.Hour,
	})
	require.NoError(t, err)

	assert.Equal(t, "tskey-auth-abc123", key)
	assert.Equal(t, http.MethodPost, recorded.method)
	assert.Equal(t, "/api/v1/preauthkey", recorded.path)
	assert.Equal(t, "Bearer test-api-key", recorded.authorization)
	assert.Equal(t, "application/json", recorded.contentType)
	require.Len(t, recorded.body, 4)
	assert.Equal(t, float64(1), recorded.body["user"])
	assert.Equal(t, true, recorded.body["reusable"])
	assert.Equal(t, true, recorded.body["ephemeral"])
	expirationRaw, ok := recorded.body["expiration"].(string)
	require.True(t, ok)
	expiration, err := time.Parse(time.RFC3339, expirationRaw)
	require.NoError(t, err)
	assert.WithinDuration(t, before.Add(time.Hour), expiration, time.Minute)
}

func TestCreatePreAuthKeyDefaultsTTL(t *testing.T) {
	var recorded recordedRequest
	srv := newMockServer(t, http.StatusOK, realShapeResponse, &recorded)

	before := time.Now()
	client := headscale.NewClient(srv.URL, "test-api-key")
	key, err := client.CreatePreAuthKey(context.Background(), 1, headscale.PreAuthKeyOpts{})
	require.NoError(t, err)

	assert.Equal(t, "tskey-auth-abc123", key)
	expirationRaw, ok := recorded.body["expiration"].(string)
	require.True(t, ok, "a zero TTL must still send an explicit expiration — headscale 0.29 rejects a key whose stored expiration is the zero time")
	expiration, err := time.Parse(time.RFC3339, expirationRaw)
	require.NoError(t, err)
	assert.WithinDuration(t, before.Add(24*time.Hour), expiration, time.Minute)
	assert.Equal(t, false, recorded.body["reusable"])
	assert.Equal(t, false, recorded.body["ephemeral"])
}

func TestCreatePreAuthKeyNon2xxCarriesStatusAndBody(t *testing.T) {
	var recorded recordedRequest
	srv := newMockServer(t, http.StatusInternalServerError, `{"code":13,"message":"creating preauthkey: boom"}`, &recorded)

	client := headscale.NewClient(srv.URL, "test-api-key")
	_, err := client.CreatePreAuthKey(context.Background(), 1, headscale.PreAuthKeyOpts{TTL: time.Hour})
	require.Error(t, err)
	assert.ErrorIs(t, err, headscale.ErrRequestFailed)
	assert.Contains(t, err.Error(), "500")
	assert.Contains(t, err.Error(), "boom")
}

func TestCreatePreAuthKeyRejectsBadJSON(t *testing.T) {
	var recorded recordedRequest
	srv := newMockServer(t, http.StatusOK, `{"preAuthKey":`, &recorded)

	client := headscale.NewClient(srv.URL, "test-api-key")
	_, err := client.CreatePreAuthKey(context.Background(), 1, headscale.PreAuthKeyOpts{TTL: time.Hour})
	require.Error(t, err)
	assert.ErrorIs(t, err, headscale.ErrMalformedResponse)
}

func TestCreatePreAuthKeyRejectsMissingKey(t *testing.T) {
	var recorded recordedRequest
	srv := newMockServer(t, http.StatusOK, `{"preAuthKey":{"id":"7"}}`, &recorded)

	client := headscale.NewClient(srv.URL, "test-api-key")
	_, err := client.CreatePreAuthKey(context.Background(), 1, headscale.PreAuthKeyOpts{TTL: time.Hour})
	require.Error(t, err)
	assert.ErrorIs(t, err, headscale.ErrMalformedResponse)
}
