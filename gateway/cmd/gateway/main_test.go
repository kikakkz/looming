// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGuideHandlerEnvMatrix pins the GET / wiring contract: no
// GATEWAY_TOPOLOGY_URL → not-configured stub; URL without token →
// fail-fast; TTL malformed → fail-fast; full config → a live page
// served end to end through the real client against a fake topologyd.
func TestGuideHandlerEnvMatrix(t *testing.T) {
	t.Setenv("GATEWAY_TOPOLOGY_URL", "")
	t.Setenv("GATEWAY_TOPOLOGY_TOKEN", "")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("absent url serves the not-configured stub", func(t *testing.T) {
		h, err := guideHandler(log)
		if err != nil {
			t.Fatalf("guideHandler: %v", err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not configured") {
			t.Fatalf("want 404 stub, got %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Setenv("GATEWAY_TOPOLOGY_URL", "http://10.0.0.11:8181")

	t.Run("url without token fails fast", func(t *testing.T) {
		if _, err := guideHandler(log); err == nil || !strings.Contains(err.Error(), "GATEWAY_TOPOLOGY_TOKEN") {
			t.Fatalf("want a GATEWAY_TOPOLOGY_TOKEN config error, got %v", err)
		}
	})

	t.Setenv("GATEWAY_TOPOLOGY_TOKEN", "service-tok")

	t.Run("malformed ttl fails fast", func(t *testing.T) {
		t.Setenv("GATEWAY_GUIDE_TTL", "soon")
		if _, err := guideHandler(log); err == nil || !strings.Contains(err.Error(), "GATEWAY_GUIDE_TTL") {
			t.Fatalf("want a GATEWAY_GUIDE_TTL config error, got %v", err)
		}
		t.Setenv("GATEWAY_GUIDE_TTL", "")
	})

	t.Run("full config serves the page end to end", func(t *testing.T) {
		var gotAuth string
		topologyd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{
				"cluster_name": "smoke cluster",
				"access_public": true,
				"cli_download_url": "https://releases.example.com/looming",
				"identity_url": "http://10.0.0.12:8081",
				"gateway_url": "http://10.0.0.11:8080",
				"steps": ["one", "two", "three", "four"],
				"register_hint": "ask an admin"
			}`))
		}))
		defer topologyd.Close()
		t.Setenv("GATEWAY_TOPOLOGY_URL", topologyd.URL)

		h, err := guideHandler(log)
		if err != nil {
			t.Fatalf("guideHandler: %v", err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d %q", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "smoke cluster") {
			t.Fatalf("page must carry the cluster name: %q", rec.Body.String())
		}
		if gotAuth != "Bearer service-tok" {
			t.Fatalf("the service token must ride the request, got %q", gotAuth)
		}
	})
}

// TestGuideRouteOnlyClaimsRoot pins the mux contract: the guide
// handler owns exactly GET / and nothing else — every other path
// falls through to the front pipeline.
func TestGuideRouteOnlyClaimsRoot(t *testing.T) {
	guide := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("guide page"))
	})
	front := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("front pipeline"))
	})
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", guide)
	mux.Handle("/", front)

	cases := []struct {
		path string
		want string
	}{
		{"/", "guide page"},
		{"/v1/chat/completions", "front pipeline"},
		{"/anything", "front pipeline"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Body.String() != tc.want {
			t.Fatalf("GET %s: want %q, got %q", tc.path, tc.want, rec.Body.String())
		}
	}

	// POST / is the front's: the guide route is GET-only.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	if rec.Body.String() != "front pipeline" {
		t.Fatalf("POST / must reach the front, got %q", rec.Body.String())
	}
}

// TestLoadUpstreamEnvMatrix pins the upstream wiring contract: the
// static auth value may ride plain HTTP only behind the explicit
// trusted-network opt-out — the CWE-319 gate production wiring relies
// on (the bundle e2e's loopback deployment is the opt-out's consumer).
func TestLoadUpstreamEnvMatrix(t *testing.T) {
	t.Setenv("GATEWAY_UPSTREAM", "")
	t.Setenv("GATEWAY_UPSTREAM_AUTH", "")
	t.Setenv("GATEWAY_UPSTREAM_INSECURE", "")

	t.Run("absent upstream fails fast", func(t *testing.T) {
		if _, _, err := loadUpstream(); err == nil || !strings.Contains(err.Error(), "GATEWAY_UPSTREAM") {
			t.Fatalf("want a GATEWAY_UPSTREAM config error, got %v", err)
		}
	})

	t.Run("auth over https is fine", func(t *testing.T) {
		t.Setenv("GATEWAY_UPSTREAM", "https://api.example.com")
		t.Setenv("GATEWAY_UPSTREAM_AUTH", "sk-static")
		upstream, auth, err := loadUpstream()
		if err != nil {
			t.Fatalf("loadUpstream: %v", err)
		}
		if upstream.Scheme != "https" || auth != "sk-static" {
			t.Fatalf("want https upstream with the static auth, got %v %q", upstream, auth)
		}
	})

	t.Run("auth over plain http fails closed", func(t *testing.T) {
		t.Setenv("GATEWAY_UPSTREAM", "http://host.docker.internal:4000")
		t.Setenv("GATEWAY_UPSTREAM_AUTH", "sk-static")
		if _, _, err := loadUpstream(); err == nil || !strings.Contains(err.Error(), "GATEWAY_UPSTREAM_INSECURE") {
			t.Fatalf("want a GATEWAY_UPSTREAM_INSECURE config error, got %v", err)
		}
	})

	t.Run("auth over plain http opts out explicitly", func(t *testing.T) {
		t.Setenv("GATEWAY_UPSTREAM", "http://host.docker.internal:4000")
		t.Setenv("GATEWAY_UPSTREAM_AUTH", "sk-static")
		t.Setenv("GATEWAY_UPSTREAM_INSECURE", "1")
		upstream, auth, err := loadUpstream()
		if err != nil {
			t.Fatalf("loadUpstream: %v", err)
		}
		if upstream.Scheme != "http" || auth != "sk-static" {
			t.Fatalf("want the trusted-network opt-out to hold, got %v %q", upstream, auth)
		}
	})

	t.Run("no auth over plain http stays allowed", func(t *testing.T) {
		t.Setenv("GATEWAY_UPSTREAM", "http://localhost:4000")
		t.Setenv("GATEWAY_UPSTREAM_AUTH", "")
		t.Setenv("GATEWAY_UPSTREAM_INSECURE", "")
		if _, _, err := loadUpstream(); err != nil {
			t.Fatalf("loadUpstream: %v", err)
		}
	})
}
