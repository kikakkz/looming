// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"testing"
	"time"
)

func TestBackofferBoundsDelays(t *testing.T) {
	b := Backoffer{Base: 100 * time.Millisecond, Cap: time.Second}
	if got := b.DelayFor(0); got != 100*time.Millisecond {
		t.Fatalf("attempt 0: got %v", got)
	}
	if got := b.DelayFor(3); got != 800*time.Millisecond {
		t.Fatalf("attempt 3: got %v", got)
	}
	if got := b.DelayFor(100); got != time.Second {
		t.Fatalf("cap breached: got %v", got)
	}
}
