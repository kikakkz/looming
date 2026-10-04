// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"log/slog"

	"github.com/kikakkz/looming/gateway/internal/front/domain"
	"github.com/kikakkz/looming/gateway/internal/front/port"
)

// ChanQueue is a bounded channel-backed interaction queue. Enqueue is
// non-blocking: a full buffer raises an explicit error log (pattern #4
// — never a silent drop). The consumer drains via C.
type ChanQueue struct {
	C chan domain.InteractionBody
}

// NewChanQueue creates a queue with the given buffer size.
func NewChanQueue(buffer int) *ChanQueue {
	return &ChanQueue{C: make(chan domain.InteractionBody, buffer)}
}

// Enqueue never blocks the data plane.
func (q *ChanQueue) Enqueue(_ context.Context, body domain.InteractionBody) {
	select {
	case q.C <- body:
	default:
		slog.Error("interaction queue full — event dropped loudly",
			"subject", body.Subject, "model", body.Model)
	}
}

var _ port.InteractionQueue = (*ChanQueue)(nil)

// LogMeter is the slice-1 meter sink: structured log lines stand in
// for the records-plane emitter (lands with the transport slice).
type LogMeter struct{}

// Record logs the meter line; real transport later.
func (LogMeter) Record(_ context.Context, m domain.MeterRecord) {
	slog.Info("meter",
		"subject", m.Subject, "model", m.Model, "outcome", m.Outcome)
}

var _ port.MeterSink = LogMeter{}
