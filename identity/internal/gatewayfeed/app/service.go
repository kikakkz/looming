// SPDX-License-Identifier: Apache-2.0

// Package app orchestrates the gateway-facing read side (identity-l1
// §5): the feed snapshot, the Consul-style blocking watch, and key
// validation. The Hub is the in-process revision authority every
// mutating capability bumps after a successful write.
package app

import (
	"context"
	"sync"
	"time"

	"github.com/kikakkz/looming/identity/internal/gatewayfeed/domain"
	"github.com/kikakkz/looming/identity/internal/gatewayfeed/port"
)

// Hub is the feed's revision authority: a monotonic counter plus a
// broadcast that wakes every waiting watch. Revision is in-process and
// monotonic; an identity restart resets it, and a response with Rev <
// the consumer's last-seen Rev is the documented full-resync signal
// (the consumer refetches with since_rev=0).
type Hub struct {
	mu  sync.Mutex
	rev uint64
	ch  chan struct{} // closed on Bump, immediately replaced
}

// NewHub starts a hub at revision 0.
func NewHub() *Hub {
	return &Hub{ch: make(chan struct{})}
}

// Bump advances the revision and wakes every waiter.
func (h *Hub) Bump() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rev++
	close(h.ch)
	h.ch = make(chan struct{})
}

// Rev returns the current revision.
func (h *Hub) Rev() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rev
}

// WaitNext blocks until the revision advances past sinceRev, the
// context ends, or (via the caller's deadline) a timeout elapses. The
// changed/newer state is NOT guaranteed on return — callers re-read
// the authoritative state themselves (the feed always returns a full
// snapshot, so a re-read is cheap and race-free).
func (h *Hub) WaitNext(ctx context.Context, sinceRev uint64) {
	for {
		h.mu.Lock()
		rev, ch := h.rev, h.ch
		h.mu.Unlock()
		if rev > sinceRev {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ch:
			// Loop and re-check: the bump may not have advanced past
			// sinceRev (an older-than-since change), or several bumps
			// may have collapsed into one wake.
		}
	}
}

// Service builds feed snapshots and answers validation lookups over
// the store, stamping every snapshot with the hub's current revision.
type Service struct {
	store        port.Store
	hub          *Hub
	watchTimeout time.Duration
}

// NewService wires the service. watchTimeout bounds the blocking-watch
// hold; identityd's loadConfig enforces it below the HTTP server's
// write timeout so a held watch can always flush.
func NewService(store port.Store, hub *Hub, watchTimeout time.Duration) *Service {
	return &Service{store: store, hub: hub, watchTimeout: watchTimeout}
}

// Hub exposes the revision authority so cmd can hand it to the
// mutating capabilities' notifiers.
func (s *Service) Hub() *Hub { return s.hub }

// Snapshot returns the full current projection stamped with the hub's
// revision. The revision is read before the store so a concurrent bump
// self-heals as one extra immediate watch cycle rather than a
// permanently missing delta.
func (s *Service) Snapshot(ctx context.Context) (*domain.Snapshot, error) {
	rev := s.hub.Rev()
	keys, err := s.store.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	principals, err := s.store.ListPrincipals(ctx)
	if err != nil {
		return nil, err
	}
	return &domain.Snapshot{Rev: rev, Keys: keys, Principals: principals}, nil
}

// Watch implements the blocking-query shape (Consul's precedent): when
// the current revision has advanced past sinceRev the snapshot returns
// immediately; otherwise the call holds until a bump or the watch
// timeout elapses, then returns the current snapshot regardless. A
// cancelled context returns the current snapshot too — the client
// either way holds a consistent full projection, never an error-shaped
// hole.
func (s *Service) Watch(ctx context.Context, sinceRev uint64) (*domain.Snapshot, error) {
	if s.hub.Rev() <= sinceRev {
		waitCtx, cancel := context.WithTimeout(ctx, s.watchTimeout)
		defer cancel()
		s.hub.WaitNext(waitCtx, sinceRev)
	}
	return s.Snapshot(ctx)
}

// Validate resolves a key hash to its principal. Fail closed in every
// direction — revoked key, non-active principal, unknown hash: one
// 404 shape with no existence signal (AD-32's northbound sameness
// rule), logged distinctions stay server-side.
func (s *Service) Validate(ctx context.Context, hash []byte) (string, error) {
	k, principalStatus, err := s.store.ByHash(ctx, hash)
	if err != nil {
		return "", err
	}
	if k.Status != domain.KeyActive || principalStatus != domain.PrincipalActive {
		return "", domain.ErrNotFound
	}
	return k.PrincipalID, nil
}
