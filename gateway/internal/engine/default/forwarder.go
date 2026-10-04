// SPDX-License-Identifier: Apache-2.0

package defaultengine

import (
	"context"
	"net/http"

	"github.com/kikakkz/looming/gateway/internal/engineplane"
)

// forwarder dispatches to the reverse proxy or reports the admin-only
// build.
func (e *Engine) forwarder(_ context.Context, w http.ResponseWriter, r *http.Request) error {
	if e.proxy == nil {
		return engineplane.ErrNotImplemented
	}
	e.proxy.ServeHTTP(w, r)
	return nil
}
