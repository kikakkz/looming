// SPDX-License-Identifier: Apache-2.0

package port

import "net/http"

// ModelExtractor pulls the requested model id from the request. The
// implementation must restore the body (upstream forwarding re-reads
// it) and return an error for unparseable bodies.
type ModelExtractor interface {
	Extract(r *http.Request) (model string, err error)
}
