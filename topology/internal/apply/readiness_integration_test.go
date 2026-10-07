// SPDX-License-Identifier: Apache-2.0

//go:build integration

package apply

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProbeIdentitydReady exercises the production readiness probe (the
// IdentitydReady seam's StdDeps wiring) against a real HTTP surface:
// ANY answer — even a 404 on an unrouted path — proves identityd is
// serving, while a refused dial reports not-yet (#131).
func TestProbeIdentitydReady(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real listener required")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	assert.True(t, probeIdentitydReady(context.Background(), srv.URL),
		"any HTTP response — a 404 included — proves the surface is serving")

	dead := freePort(t)
	assert.False(t, probeIdentitydReady(context.Background(), fmt.Sprintf("http://127.0.0.1:%d", dead)),
		"a refused dial reports not-yet")

	hang, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = hang.Close() }()
	assert.False(t, probeIdentitydReady(context.Background(), fmt.Sprintf("http://%s", hang.Addr().String())),
		"a port that accepts but never answers HTTP is not serving")
}
