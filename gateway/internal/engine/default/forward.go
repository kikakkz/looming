// SPDX-License-Identifier: Apache-2.0

package defaultengine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// ErrUpstream marks a forwarding failure (transport error, unreachable
// upstream). Client aborts (context cancellation) are not failures.
var ErrUpstream = errors.New("defaultengine: upstream forwarding failed")

// failKey carries a per-request failure flag from forwarder to the
// proxy's ErrorHandler — the shared proxy is never mutated per request.
type failKey struct{}

// Forward is the default engine's data face: a payload-preserving
// reverse proxy to the configured upstream (AD-32: model ids and bodies
// travel untouched). httputil streams request/response bodies without
// rewriting and flushes SSE per write.
//
// Credential hygiene is fail-closed: the gateway's bearer token NEVER
// travels upstream. upstreamAuth configures the credential the upstream
// expects; empty strips Authorization entirely.
func (e *Engine) Forward(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	return e.forwarder(ctx, w, r)
}

// NewForwardProxy builds the reverse proxy against an upstream base
// URL. Paths pass through as-is; the outbound Host header targets the
// upstream (hosted providers route by Host); transport failures set a
// per-request flag (via request context — shared-proxy safe) so the
// caller learns the call never reached a model.
func NewForwardProxy(upstream *url.URL, upstreamAuth string) *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := rp.Director
	rp.Director = func(r *http.Request) {
		originalDirector(r)
		r.Host = upstream.Host
		if upstreamAuth != "" {
			r.Header.Set("Authorization", "Bearer "+upstreamAuth)
		} else {
			r.Header.Del("Authorization")
		}
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return // client went away: nothing actionable
		}
		if fb, ok := r.Context().Value(failKey{}).(*bool); ok {
			*fb = true
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}
	rp.FlushInterval = 0 // immediate flush: streaming responses pass through per write
	return rp
}
