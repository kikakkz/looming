// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"log/slog"
)

// DrainInteractions consumes the queue until ctx ends. Slice-1 stand-in
// for the records-plane transport: structured logs keep interactions
// observable without ever blocking the data plane.
func DrainInteractions(ctx context.Context, q *ChanQueue, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case body := <-q.C:
			log.Info("interaction",
				"subject", body.Subject,
				"model", body.Model,
				"request_bytes", len(body.RequestBody),
				"response_bytes", len(body.ResponseBody),
				"truncated", body.Truncated)
		}
	}
}
