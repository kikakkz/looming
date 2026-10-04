// SPDX-License-Identifier: Apache-2.0

package defaultengine

import (
	"context"
	"errors"
	"net/http"

	"github.com/kikakkz/looming/gateway/internal/engineplane"
)

// forwarder dispatches to the reverse proxy or reports the admin-only
// build. Proxy transport failures return ErrUpstream — the caller maps
// them to 502 and must not meter the call (AD-32 #5).
func (e *Engine) forwarder(_ context.Context, w http.ResponseWriter, r *http.Request) error {
	if e.proxy == nil {
		return engineplane.ErrNotImplemented
	}
	failed := false
	e.proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return // client went away: nothing actionable
		}
		failed = true
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}
	e.proxy.ServeHTTP(w, r)
	if failed {
		return ErrUpstream
	}
	return nil
}
