// SPDX-License-Identifier: Apache-2.0

package defaultengine

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestForwardProxyPassesThrough(t *testing.T) {
	const wantReq = `{"model":"gpt-5"}`
	const wantResp = "data: {\"ok\":true}\n\n"
	var got []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, wantResp)
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	engine := NewWithUpstream(u, "upstream-secret")

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(wantReq))
	rec := httptest.NewRecorder()
	if err := engine.Forward(req.Context(), rec, req); err != nil {
		t.Fatal(err)
	}
	if string(got) != wantReq {
		t.Fatalf("request body not preserved: %q", got)
	}
	if rec.Body.String() != wantResp {
		t.Fatalf("response body not preserved: %q", rec.Body.String())
	}
	// credential hygiene: upstream auth injected, gateway token absent
	if hdr := lastAuth; hdr != "Bearer upstream-secret" {
		t.Fatalf("upstream auth not injected: %q", hdr)
	}
}

var lastAuth string

func TestForwardWithoutUpstreamReportsNotImplemented(t *testing.T) {
	engine := New()
	req := httptest.NewRequest("POST", "/", nil)
	rec := httptest.NewRecorder()
	if err := engine.Forward(req.Context(), rec, req); err == nil {
		t.Fatal("admin-only engine must report ErrNotImplemented")
	}
}

func TestForwardStripsGatewayTokenWithoutUpstreamAuth(t *testing.T) {
	var gotAuth string
	var sawHeader bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, authSet := r.Header["Authorization"]
		sawHeader = authSet
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	engine := NewWithUpstream(u, "")
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer GATEWAY-SECRET-MUST-NOT-LEAK")
	rec := httptest.NewRecorder()
	if err := engine.Forward(req.Context(), rec, req); err != nil {
		t.Fatal(err)
	}
	if sawHeader || gotAuth != "" {
		t.Fatalf("gateway token must be stripped upstream: %q", gotAuth)
	}
}

func TestForwardSetsUpstreamHostHeader(t *testing.T) {
	var gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	engine := NewWithUpstream(u, "")
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	req.Host = "gateway.internal:9999"
	rec := httptest.NewRecorder()
	if err := engine.Forward(req.Context(), rec, req); err != nil {
		t.Fatal(err)
	}
	if gotHost != u.Host {
		t.Fatalf("outbound Host = %q, want upstream host %q", gotHost, u.Host)
	}
}

func TestForwardUnreachableUpstreamIsAnError(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:1") // nothing listens
	engine := NewWithUpstream(u, "")
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	if err := engine.Forward(req.Context(), rec, req); !errors.Is(err, ErrUpstream) {
		t.Fatalf("want ErrUpstream, got %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
}
