// SPDX-License-Identifier: Apache-2.0

package defaultengine

import (
	"context"
	"net/http"

	"github.com/kikakkz/looming/gateway/internal/engineplane"
)

// forwarder dispatches to the reverse proxy or reports the admin-only
// build. The failure flag lives in the request context — the shared
// proxy and its ErrorHandler are never mutated per request.
func (e *Engine) forwarder(_ context.Context, w http.ResponseWriter, r *http.Request) error {
	if e.proxy == nil {
		return engineplane.ErrNotImplemented
	}
	failed := false
	r = r.WithContext(context.WithValue(r.Context(), failKey{}, &failed))
	e.proxy.ServeHTTP(w, r)
	if failed {
		return ErrUpstream
	}
	return nil
}
