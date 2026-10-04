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

// Forward is the default engine's data face: a payload-preserving
// reverse proxy to the configured upstream (AD-32: model ids and bodies
// travel untouched). httputil streams request/response bodies without
// rewriting and flushes SSE per write.
//
// Credential hygiene is fail-closed: the gateway's bearer token NEVER
// travels upstream. WithUpstreamAuth configures the credential the
// upstream expects; without one the Authorization header is stripped.
func (e *Engine) Forward(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	return e.forwarder(ctx, w, r)
}

// NewForwardProxy builds the reverse proxy against an upstream base
// URL (e.g. https://api.deepseek.com). Paths pass through as-is; the
// outbound Host header targets the upstream (hosted providers route by
// Host); proxy transport failures surface as ErrUpstream so the caller
// never meters a call that never reached a model.
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
	rp.FlushInterval = 0 // immediate flush: streaming responses pass through per write
	return rp
}
