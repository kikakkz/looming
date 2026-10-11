// SPDX-License-Identifier: Apache-2.0

package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
	adapter "github.com/kikakkz/looming/platform/go/joinadapter"
)

// TestClientJoinCarriesObservedFacts pins the slice-1.3 wire shape: a
// non-nil capabilities block serializes into the host object, and a
// nil one leaves the key absent (the old-CLI payload, accepted by
// mixed-version topologyds).
func TestClientJoinCarriesObservedFacts(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"host_id":"host-0123abcd","credential":"cred"}`))
	}))
	defer server.Close()

	collected := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	egress := true
	_, err := adapter.NewClient(server.URL).Join(context.Background(), adapter.JoinRequest{
		Token:   "tok",
		Address: "10.0.0.21",
		Capabilities: &hostdomain.Capabilities{
			Hardware:    hostdomain.HardwareCapabilities{CPUCores: 8, MemoryMB: 32768, DiskGB: 457, Arch: "x86_64"},
			Network:     hostdomain.NetworkCapabilities{Egress: &egress},
			CollectedAt: collected,
		},
	})
	require.NoError(t, err)
	host := got["host"].(map[string]any)
	caps, ok := host["capabilities"].(map[string]any)
	require.True(t, ok, "capabilities must serialize into the host object: %v", got)
	hw := caps["hardware"].(map[string]any)
	assert.Equal(t, float64(8), hw["cpu_cores"])
	assert.Equal(t, "x86_64", hw["arch"])
	assert.Equal(t, true, caps["network"].(map[string]any)["egress"])
	assert.Equal(t, "2026-10-11T12:00:00Z", caps["collected_at"])
}

func TestClientJoinOmitsAbsentFacts(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"host_id":"host-0123abcd","credential":"cred"}`))
	}))
	defer server.Close()

	_, err := adapter.NewClient(server.URL).Join(context.Background(), adapter.JoinRequest{
		Token: "tok", Address: "10.0.0.21",
	})
	require.NoError(t, err)
	_, present := got["host"].(map[string]any)["capabilities"]
	assert.False(t, present, "absent facts must omit the key, not send null")
}

// TestClientHostsRoundTrip covers the observed-facts read: bearer
// guard wiring, envelope errors, and the capabilities decode.
func TestClientHostsRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/internal/hosts", r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer service-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"unauthenticated"}}`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"node-1","address":"10.0.0.11","labels":["role=engine"],
			 "capabilities":{"hardware":{"cpu_cores":8,"memory_mb":32768,"disk_gb":457,"arch":"x86_64"},
			                 "network":{"egress":true},
			                 "collected_at":"2026-10-11T12:00:00Z"}},
			{"id":"node-2","address":"10.0.0.12","labels":[],"capabilities":null}
		]`))
	}))
	defer server.Close()

	client := adapter.NewClient(server.URL)
	_, err := client.Hosts(context.Background(), "")
	require.Error(t, err, "the service token is mandatory")

	hosts, err := client.Hosts(context.Background(), "service-tok")
	require.NoError(t, err)
	require.Len(t, hosts, 2)
	assert.Equal(t, "node-1", hosts[0].ID)
	require.NotNil(t, hosts[0].Capabilities)
	assert.Equal(t, 8, hosts[0].Capabilities.Hardware.CPUCores)
	require.NotNil(t, hosts[0].Capabilities.Network.Egress)
	assert.True(t, *hosts[0].Capabilities.Network.Egress)
	assert.Nil(t, hosts[1].Capabilities, "an old-CLI host reports null facts")

	_, err = client.Hosts(context.Background(), "wrong")
	require.Error(t, err)
	var apiErr *adapter.Error
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnauthorized, apiErr.Status)
}

func TestClientHostsMalformedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"node-1"}]`)) // no usable capabilities key handling needed; shape is fine but empty
	}))
	defer server.Close()

	hosts, err := adapter.NewClient(server.URL).Hosts(context.Background(), "tok")
	require.NoError(t, err)
	assert.Len(t, hosts, 1)
}
