// SPDX-License-Identifier: Apache-2.0

package defaultengine

import (
	"context"
	"net/http"

	"github.com/kikakkz/looming/gateway/internal/engineplane"
)

// forwarder dispatches to the reverse proxy or reports the admin-only
// build. The proxy's ErrorHandler is configured once at construction
// (a shared ReverseProxy must not be mutated per request); per-call
// failure state rides the request context, so concurrent forwards stay
// independent. Proxy transport failures surface as ErrUpstream — the
// caller maps them to 502 and must not meter the call (AD-32 #5).
func (e *Engine) forwarder(_ context.Context, w http.ResponseWriter, r *http.Request) error {
	if e.proxy == nil {
		return engineplane.ErrNotImplemented
	}
	res, _ := r.Context().Value(forwardResultKey{}).(*forwardResult)
	e.proxy.ServeHTTP(w, r)
	if res != nil && res.failed {
		return ErrUpstream
	}
	return nil
}

// forwardResult carries one call's transport failure back from the
// proxy's ErrorHandler; the context value makes it per-request.
type forwardResult struct{ failed bool }

type forwardResultKey struct{}
