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
// travels upstream. The per-request credential (empty = unprovisioned)
// rides the request context — the stdlib-sanctioned per-request channel,
// since httputil's Director signature is fixed — and the director picks
// it up per outbound request, so concurrent forwards with distinct
// credentials never bleed. The fallback chain is: per-request
// credential → configured static upstreamAuth → strip Authorization.
func (e *Engine) Forward(ctx context.Context, w http.ResponseWriter, r *http.Request, credential string) error {
	res := &forwardResult{}
	ctx = context.WithValue(ctx, forwardResultKey{}, res)
	if credential != "" {
		ctx = context.WithValue(ctx, credentialContextKey{}, credential)
	}
	r = r.WithContext(ctx)
	return e.forwarder(ctx, w, r)
}

// credentialContextKey scopes the per-request engine credential to this
// package: unexported, so nothing outside can read or forge it.
type credentialContextKey struct{}

// NewForwardProxy builds the reverse proxy against an upstream base
// URL (e.g. https://api.deepseek.com). Paths pass through as-is; the
// outbound Host header targets the upstream (hosted providers route by
// Host); proxy transport failures surface as ErrUpstream so the caller
// never meters a call that never reached a model. The ErrorHandler is
// bound here, once: a shared ReverseProxy must not be mutated per
// request (concurrency), so per-call failure state travels in the
// request context instead.
func NewForwardProxy(upstream *url.URL, upstreamAuth string) *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := rp.Director
	rp.Director = func(r *http.Request) {
		originalDirector(r)
		r.Host = upstream.Host
		if cred, ok := r.Context().Value(credentialContextKey{}).(string); ok && cred != "" {
			r.Header.Set("Authorization", "Bearer "+cred)
			return
		}
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
		if res, ok := r.Context().Value(forwardResultKey{}).(*forwardResult); ok {
			res.failed = true
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}
	rp.FlushInterval = 0 // immediate flush: streaming responses pass through per write
	return rp
}
