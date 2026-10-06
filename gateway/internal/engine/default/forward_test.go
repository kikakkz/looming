// SPDX-License-Identifier: Apache-2.0

package defaultengine

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
	if err := engine.Forward(req.Context(), rec, req, ""); err != nil {
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
	if err := engine.Forward(req.Context(), rec, req, ""); err == nil {
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
	if err := engine.Forward(req.Context(), rec, req, ""); err != nil {
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
	if err := engine.Forward(req.Context(), rec, req, ""); err != nil {
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
	if err := engine.Forward(req.Context(), rec, req, ""); !errors.Is(err, ErrUpstream) {
		t.Fatalf("want ErrUpstream, got %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
}

// authRecorder is an upstream that records the Authorization header of
// every request it receives, keyed by an X-Seq probe header the tests
// set on the inbound request.
type authRecorder struct {
	mu    sync.Mutex
	auths map[string]string
	srv   *httptest.Server
}

func newAuthRecorder(t *testing.T) *authRecorder {
	t.Helper()
	a := &authRecorder{auths: map[string]string{}}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.auths[r.Header.Get("X-Seq")] = r.Header.Get("Authorization")
		a.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *authRecorder) authFor(seq string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.auths[seq]
}

// TestForwardPerRequestCredentialOverridesStatic pins the slice-C
// contract: an explicit per-call credential becomes the upstream
// Authorization, replacing the configured static one. The gateway
// token on the inbound request must still never travel.
func TestForwardPerRequestCredentialOverridesStatic(t *testing.T) {
	rec := newAuthRecorder(t)
	u, _ := url.Parse(rec.srv.URL)
	engine := NewWithUpstream(u, "static-upstream-secret")

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer GATEWAY-LOOMING-KEY-MUST-NOT-LEAK")
	req.Header.Set("X-Seq", "cred")
	rrec := httptest.NewRecorder()
	if err := engine.Forward(req.Context(), rrec, req, "per-key-engine-cred"); err != nil {
		t.Fatal(err)
	}
	if got := rec.authFor("cred"); got != "Bearer per-key-engine-cred" {
		t.Fatalf("per-request credential must win over the static auth, got %q", got)
	}
}

// TestForwardEmptyCredentialFallsBackToStatic pins the fallback half of
// the contract: an empty credential means "unprovisioned" and the
// static upstreamAuth applies exactly as before slice C.
func TestForwardEmptyCredentialFallsBackToStatic(t *testing.T) {
	rec := newAuthRecorder(t)
	u, _ := url.Parse(rec.srv.URL)
	engine := NewWithUpstream(u, "static-upstream-secret")

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("X-Seq", "fallback")
	rrec := httptest.NewRecorder()
	if err := engine.Forward(req.Context(), rrec, req, ""); err != nil {
		t.Fatal(err)
	}
	if got := rec.authFor("fallback"); got != "Bearer static-upstream-secret" {
		t.Fatalf("empty credential must fall back to the static auth, got %q", got)
	}
}

// TestForwardEmptyCredentialWithoutStaticStripsAuthorization pins the
// tail of the fallback chain: no per-request credential and no static
// auth means the upstream sees no Authorization header at all.
func TestForwardEmptyCredentialWithoutStaticStripsAuthorization(t *testing.T) {
	rec := newAuthRecorder(t)
	u, _ := url.Parse(rec.srv.URL)
	engine := NewWithUpstream(u, "")

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer GATEWAY-LOOMING-KEY-MUST-NOT-LEAK")
	req.Header.Set("X-Seq", "strip")
	rrec := httptest.NewRecorder()
	if err := engine.Forward(req.Context(), rrec, req, ""); err != nil {
		t.Fatal(err)
	}
	if got, ok := rec.auths["strip"]; !ok || got != "" {
		t.Fatalf("no credential anywhere must strip Authorization, got %q", got)
	}
}

// TestForwardConcurrentMixedCredentialsNoBleed runs mixed traffic —
// distinct per-request credentials, empty-credential fallbacks, and
// inbound gateway keys — through the SHARED proxy concurrently. Under
// -race this proves the per-request credential is carried per request
// and never bleeds across goroutines.
func TestForwardConcurrentMixedCredentialsNoBleed(t *testing.T) {
	rec := newAuthRecorder(t)
	u, _ := url.Parse(rec.srv.URL)
	engine := NewWithUpstream(u, "static-upstream-secret")

	const goroutines = 12
	const perGoroutine = 10
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perGoroutine)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				seq := fmt.Sprintf("g%d-%d", g, i)
				req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
				req.Header.Set("Authorization", "Bearer gateway-key-"+seq)
				req.Header.Set("X-Seq", seq)
				rrec := httptest.NewRecorder()
				cred := "engine-cred-" + seq
				if g%3 == 0 {
					cred = "" // fallback to the static auth
				}
				if err := engine.Forward(req.Context(), rrec, req, cred); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for g := 0; g < goroutines; g++ {
		for i := 0; i < perGoroutine; i++ {
			seq := fmt.Sprintf("g%d-%d", g, i)
			want := "Bearer engine-cred-" + seq
			if g%3 == 0 {
				want = "Bearer static-upstream-secret"
			}
			if got := rec.authFor(seq); got != want {
				t.Fatalf("seq %s: want %q, got %q (cross-request bleed or wrong fallback)", seq, want, got)
			}
		}
	}
}
