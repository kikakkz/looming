// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	app "github.com/kikakkz/looming/platform/go/hostapp"
	domain "github.com/kikakkz/looming/platform/go/hostdomain"
)

// handlerRegistry is the Registry double for the read-path tests: a
// scripted list plus the write surface the port demands (unused here).
type handlerRegistry struct {
	hosts []domain.Host
	err   error
}

func (f *handlerRegistry) Register(_ context.Context, h *domain.Host) (*domain.Host, error) {
	return h, nil
}

func (f *handlerRegistry) ByID(context.Context, string) (*domain.Host, error) {
	return nil, domain.ErrNotFound
}

func (f *handlerRegistry) ByAddress(context.Context, string) (*domain.Host, error) {
	return nil, domain.ErrNotFound
}

func (f *handlerRegistry) Update(_ context.Context, h *domain.Host) (*domain.Host, error) {
	return h, nil
}

func (f *handlerRegistry) List(context.Context) ([]domain.Host, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.hosts, nil
}

func getHosts(t *testing.T, handler *app.Handler, token string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/internal/hosts", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.Hosts(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, body
}

func TestHostsServesObservedFactsWithoutCredentials(t *testing.T) {
	egress := true
	collected := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	reg := &handlerRegistry{hosts: []domain.Host{{
		ID:             "node-1",
		Address:        "10.0.0.11",
		RoleLabels:     []string{"role=engine"},
		CredentialHash: []byte("never on the wire"),
		Capabilities: &domain.Capabilities{
			Hardware:    domain.HardwareCapabilities{CPUCores: 8, MemoryMB: 32768, DiskGB: 457, Arch: "x86_64"},
			Network:     domain.NetworkCapabilities{Egress: &egress},
			CollectedAt: collected,
		},
	}}}
	handler := app.NewHandler(app.NewService(reg), "service-tok")

	status, body := getHosts(t, handler, "service-tok")
	require.Equal(t, http.StatusOK, status)

	var hosts []map[string]any
	require.NoError(t, json.Unmarshal(body, &hosts))
	require.Len(t, hosts, 1)
	assert.Equal(t, "node-1", hosts[0]["id"])
	assert.Equal(t, "10.0.0.11", hosts[0]["address"])
	assert.Equal(t, []any{"role=engine"}, hosts[0]["labels"])
	caps, ok := hosts[0]["capabilities"].(map[string]any)
	require.True(t, ok, "capabilities must round trip: %s", body)
	hw := caps["hardware"].(map[string]any)
	assert.Equal(t, float64(8), hw["cpu_cores"])
	assert.Equal(t, "x86_64", hw["arch"])
	assert.Equal(t, "2026-10-11T12:00:00Z", caps["collected_at"])
	assert.NotContains(t, string(body), "never on the wire", "the credential never leaves the store")
}

// TestHostsGuardIsTheGuideShape pins the shared guard contract: no
// configured token means 503 (the deployment cannot publish), a wrong
// or missing bearer means 401, and a store failure is a logged 500
// with the house envelope.
func TestHostsGuardIsTheGuideShape(t *testing.T) {
	reg := &handlerRegistry{hosts: []domain.Host{{ID: "node-1", Address: "10.0.0.11"}}}

	unconfigured := app.NewHandler(app.NewService(reg), "")
	status, body := getHosts(t, unconfigured, "anything")
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Contains(t, string(body), "hosts_unavailable")

	handler := app.NewHandler(app.NewService(reg), "service-tok")
	for _, token := range []string{"", "wrong"} {
		gotStatus, gotBody := getHosts(t, handler, token)
		assert.Equal(t, http.StatusUnauthorized, gotStatus, "token %q", token)
		assert.Contains(t, string(gotBody), "unauthenticated")
	}

	failing := app.NewHandler(app.NewService(&handlerRegistry{err: errors.New("db down")}), "service-tok")
	status, body = getHosts(t, failing, "service-tok")
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Contains(t, string(body), `"internal"`)
}

// TestHostsNullCapabilitiesRoundTrip pins the old-CLI row shape: a
// host that joined without facts reports capabilities null, and the
// empty registry reports an empty list (not null).
func TestHostsNullCapabilitiesRoundTrip(t *testing.T) {
	reg := &handlerRegistry{hosts: []domain.Host{{ID: "node-1", Address: "10.0.0.11"}}}
	handler := app.NewHandler(app.NewService(reg), "service-tok")

	status, body := getHosts(t, handler, "service-tok")
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, string(body), `"capabilities":null`)

	empty := app.NewHandler(app.NewService(&handlerRegistry{}), "service-tok")
	_, body = getHosts(t, empty, "service-tok")
	assert.Contains(t, string(body), `[]`)
}
