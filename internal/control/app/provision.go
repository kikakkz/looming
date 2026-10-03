// Package app holds the gateway control plane: read-only projection
// caches (identity, engine config) and the provisioning channel. It
// owns no policy data — every cache is a projection with a named
// upstream authority (gateway-l1 §4).
package app

import (
	"context"
	"errors"
	"time"

	"github.com/kikakkz/looming/internal/control/domain"
)

// Provisioner drives Journey 2's engine writes. Engine calls land in
// a later slice; the retry skeleton is exercised now.
type Provisioner struct {
	admin    EngineAdminCaller
	backoff  domain.Backoffer
	failures chan<- domain.FailureEvent
}

// EngineAdminCaller abstracts the engine admin call for this package.
// Implemented by adapters over EnginePlane.Admin.
type EngineAdminCaller interface {
	ProvisionKey(ctx context.Context, subject string) error
}

// ErrAttemptsExhausted marks a provisioning operation that exhausted
// its retries; a FailureEvent accompanies it.
var ErrAttemptsExhausted = errors.New("cp: provisioning attempts exhausted")

// MaxAttempts bounds one provisioning operation.
const MaxAttempts = 5

// NewProvisioner wires a provisioner; failures may be nil (events
// dropped only when nobody listens — the error still propagates).
func NewProvisioner(a EngineAdminCaller, b domain.Backoffer, failures chan<- domain.FailureEvent) *Provisioner {
	return &Provisioner{admin: a, backoff: b, failures: failures}
}

// Provision runs the bounded-retry loop. Every attempt failure is
// retried with backoff; exhausting MaxAttempts emits a FailureEvent
// and returns ErrAttemptsExhausted wrapping the last error.
func (p *Provisioner) Provision(ctx context.Context, subject string) error {
	var lastErr error
	for attempt := 0; attempt < MaxAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(p.backoff.DelayFor(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := p.admin.ProvisionKey(ctx, subject); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if p.failures != nil {
		select {
		case p.failures <- domain.FailureEvent{Op: "provision", Subject: subject, Err: lastErr}:
		case <-ctx.Done():
		}
	}
	return errors.Join(ErrAttemptsExhausted, lastErr)
}
