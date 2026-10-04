// SPDX-License-Identifier: Apache-2.0

package defaultengine

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Forward is the default engine's data face: a payload-preserving
// reverse proxy to the configured upstream (AD-32: model ids and bodies
// travel untouched). httputil streams request/response bodies without
// rewriting and flushes SSE per write.
func (e *Engine) Forward(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	return e.forwarder(ctx, w, r)
}

// NewForwardProxy builds the reverse proxy against an upstream base
// URL (e.g. https://api.deepseek.com). Paths pass through as-is.
func NewForwardProxy(upstream *url.URL) *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := rp.Director
	rp.Director = func(r *http.Request) {
		originalDirector(r)
		// Preserve the client's Authorization for the upstream; the
		// engine's own credential exchange replaces it in a later slice.
	}
	rp.FlushInterval = 0 // immediate flush: streaming responses pass through per write
	return rp
}
