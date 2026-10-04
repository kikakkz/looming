// SPDX-License-Identifier: Apache-2.0

package port

import (
	"context"

	"github.com/kikakkz/looming/gateway/internal/front/domain"
)

// InteractionQueue accepts assembled interactions. Implementations
// must be non-blocking on the data plane: enqueue with a bounded buffer
// and raise an explicit failure (never drop silently) when full
// (docs/component-patterns.md #4).
type InteractionQueue interface {
	Enqueue(ctx context.Context, body domain.InteractionBody)
}

// MeterSink receives inline metering. Implementations must be cheap and
// non-blocking; usage counting is complete-call-only (AD-32 #5).
type MeterSink interface {
	Record(ctx context.Context, m domain.MeterRecord)
}
