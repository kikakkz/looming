// SPDX-License-Identifier: Apache-2.0

package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapter "github.com/kikakkz/looming/platform/go/joinadapter"
)

func TestClientJoinHappyPath(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/join", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"host_id":"host-0123abcd","credential":"cred","cluster":{"access":"http://10.0.0.10:8080"}}`))
	}))
	defer server.Close()

	res, err := adapter.NewClient(server.URL).Join(context.Background(), adapter.JoinRequest{
		Token:   "tok",
		HostID:  "host-op",
		Address: "10.0.0.21",
		Labels:  []string{"gpu"},
	})
	require.NoError(t, err)
	assert.Equal(t, "host-0123abcd", res.HostID)
	assert.Equal(t, "cred", res.Credential)
	assert.Equal(t, "http://10.0.0.10:8080", res.Access)

	host := got["host"].(map[string]any)
	assert.Equal(t, "tok", got["token"])
	assert.Equal(t, "host-op", host["id"])
	assert.Equal(t, "10.0.0.21", host["address"])
	assert.Equal(t, []any{"gpu"}, host["labels"])
}

func TestClientJoinErrorEnvelope(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"token expired", http.StatusForbidden, `{"error":{"code":"token_expired","message":"expired"}}`, "token_expired"},
		{"token used", http.StatusConflict, `{"error":{"code":"token_used","message":"token_used"}}`, "token_used"},
		{"address taken", http.StatusConflict, `{"error":{"code":"address_taken","message":"address taken: re-join with the existing host credential"}}`, "address_taken"},
		{"malformed body", http.StatusInternalServerError, `<html>nope</html>`, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			_, err := adapter.NewClient(server.URL).Join(context.Background(), adapter.JoinRequest{
				Token: "tok", Address: "10.0.0.21",
			})
			require.Error(t, err)
			var apiErr *adapter.Error
			require.True(t, errors.As(err, &apiErr), "must surface *adapter.Error, got %T", err)
			assert.Equal(t, tc.status, apiErr.Status)
			assert.Equal(t, tc.code, apiErr.Code)
		})
	}
}

func TestClientJoinUnreachableServer(t *testing.T) {
	// Port 1 is the discarded TCP port: always refused.
	_, err := adapter.NewClient("http://127.0.0.1:1").Join(context.Background(), adapter.JoinRequest{
		Token: "tok", Address: "10.0.0.21",
	})
	require.Error(t, err)
	var apiErr *adapter.Error
	assert.False(t, errors.As(err, &apiErr), "transport failures are not server envelopes")
	assert.Contains(t, err.Error(), "join: request:")
}

func TestClientJoinEmptyHostIDRejectedByServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"credential":"cred"}`)) // no host_id
	}))
	defer server.Close()

	_, err := adapter.NewClient(server.URL).Join(context.Background(), adapter.JoinRequest{
		Token: "tok", Address: "10.0.0.21",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "201 without a usable body")
}
