// Package domain holds the control plane's pure rules: revision
// semantics live with the cache; backoff and failure signalling live
// here (docs/component-patterns.md #4: no silent drops).
package domain

import "time"

// FailureEvent is the explicit signal a failed-and-exhausted operation
// emits (docs/component-patterns.md #4: no silent drops). The channel
// belongs to the operator's alerting surface.
type FailureEvent struct {
	Op      string
	Subject string
	Err     error
}

// Backoffer produces retry delays: bounded exponential backoff.
// Attempt i returns min(base * 2^i, cap); it never returns a negative
// or zero delay for i > 0, and callers stop when ctx ends.
type Backoffer struct {
	Base time.Duration
	Cap  time.Duration
}

// DelayFor returns the delay before attempt i (i starts at 0).
func (b Backoffer) DelayFor(attempt int) time.Duration {
	d := b.Base
	for i := 0; i < attempt && d < b.Cap; i++ {
		d *= 2
	}
	if d > b.Cap {
		return b.Cap
	}
	return d
}
