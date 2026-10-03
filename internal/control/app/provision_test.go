package app

import (
	"context"
	"errors"
	"github.com/kikakkz/looming/internal/control/domain"
	"testing"
	"time"
)

type flakyAdmin struct {
	failures int
	calls    int
}

func (f *flakyAdmin) ProvisionKey(_ context.Context, _ string) error {
	f.calls++
	if f.calls <= f.failures {
		return errors.New("engine admin unavailable")
	}
	return nil
}

func TestProvisionerSucceedsAfterRetries(t *testing.T) {
	admin := &flakyAdmin{failures: 2}
	p := NewProvisioner(admin, domain.Backoffer{Base: time.Millisecond, Cap: 5 * time.Millisecond}, nil)
	if err := p.Provision(context.Background(), "ker"); err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if admin.calls != 3 {
		t.Fatalf("want 3 calls, got %d", admin.calls)
	}
}

func TestProvisionerExhaustionEmitsFailureEvent(t *testing.T) {
	admin := &flakyAdmin{failures: 99}
	failures := make(chan domain.FailureEvent, 1)
	p := NewProvisioner(admin, domain.Backoffer{Base: time.Millisecond, Cap: 2 * time.Millisecond}, failures)
	err := p.Provision(context.Background(), "ker")
	if !errors.Is(err, ErrAttemptsExhausted) {
		t.Fatalf("want ErrAttemptsExhausted, got %v", err)
	}
	select {
	case ev := <-failures:
		if ev.Subject != "ker" || ev.Err == nil {
			t.Fatalf("failure event must carry subject and error, got %+v", ev)
		}
	default:
		t.Fatal("a FailureEvent must be emitted on exhaustion")
	}
}

func TestBackofferBoundsDelays(t *testing.T) {
	b := domain.Backoffer{Base: 100 * time.Millisecond, Cap: time.Second}
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
