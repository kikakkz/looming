// SPDX-License-Identifier: Apache-2.0

package adapter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/gateway/internal/front/adapter"
)

const guidePayload = `{
  "cluster_name": "prod cluster",
  "access_public": true,
  "cli_download_url": "https://releases.example.com/looming",
  "identity_url": "http://10.0.0.12:8081",
  "gateway_url": "http://10.0.0.11:8080",
  "steps": ["one", "two"],
  "register_hint": "ask an admin"
}`

func TestGuideClientFetchesSnapshot(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		assert.Equal(t, "/v1/internal/guide", r.URL.Path)
		_, _ = w.Write([]byte(guidePayload))
	}))
	defer server.Close()

	c := adapter.NewGuideClient(server.URL, "service-tok")
	g, err := c.FetchGuide(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "Bearer service-tok", gotAuth)
	assert.Equal(t, "prod cluster", g.ClusterName)
	assert.True(t, g.AccessPublic)
	assert.Equal(t, "https://releases.example.com/looming", g.CLIDownloadURL)
	assert.Equal(t, "http://10.0.0.12:8081", g.IdentityURL)
	assert.Equal(t, "http://10.0.0.11:8080", g.GatewayURL)
	assert.Equal(t, []string{"one", "two"}, g.Steps)
	assert.Equal(t, "ask an admin", g.RegisterHint)
}

func TestGuideClientFailures(t *testing.T) {
	t.Run("5xx is an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		_, err := adapter.NewGuideClient(server.URL, "tok").FetchGuide(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "503")
	})

	t.Run("timeout is an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write([]byte(guidePayload))
		}))
		defer server.Close()
		c := adapter.NewGuideClient(server.URL, "tok")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := c.FetchGuide(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context deadline exceeded",
			"the transport cause is visible in the message even though the sentinel wraps the origin classification")
	})

	t.Run("malformed body is an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("{not json"))
		}))
		defer server.Close()
		_, err := adapter.NewGuideClient(server.URL, "tok").FetchGuide(context.Background())
		require.Error(t, err)
	})

	t.Run("bad base url is an error", func(t *testing.T) {
		_, err := adapter.NewGuideClient("http://[::1", "tok").FetchGuide(context.Background())
		require.Error(t, err)
	})
}
