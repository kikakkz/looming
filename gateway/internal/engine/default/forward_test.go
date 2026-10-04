// SPDX-License-Identifier: Apache-2.0

package defaultengine

import (
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
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, wantResp)
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	engine := NewWithUpstream(u)

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
}

func TestForwardWithoutUpstreamReportsNotImplemented(t *testing.T) {
	engine := New()
	req := httptest.NewRequest("POST", "/", nil)
	rec := httptest.NewRecorder()
	if err := engine.Forward(req.Context(), rec, req); err == nil {
		t.Fatal("admin-only engine must report ErrNotImplemented")
	}
}
