// SPDX-License-Identifier: Apache-2.0
// Package cp holds the gateway control plane: read-only projection
// caches (identity, engine config) and the provisioning channel. It
// owns no policy data — every cache is a projection with a named
// upstream authority (gateway-l1 §4).
package app

import (
	"context"
	"sync"
)

// Revision is monotonic per source (docs/component-patterns.md #3):
// readers suppress stale snapshots; gaps are the syncer's signal.
type Revision uint64

// Snapshot is a shared cache view: clone-on-store isolates the writer
// from readers, but recipients MUST NOT mutate V — Go cannot enforce
// immutability, so the contract is documented and reviewed.
type Snapshot struct {
	Rev Revision
	V   map[string][]string
}

type update struct {
	rev     Revision
	subject string
	models  []string
	ack     chan struct{}
}

// ModelAllowlistCache caches subject → model allowlists projected from
// the identity/registry authority. Single-writer event loop
// (component-patterns #2): one goroutine owns the map; readers take
// immutable snapshots. No locks across I/O; stale (non-monotonic)
// revisions are dropped. The keyHash → principal projection is a
// separate cache (keycache.go) — two distinct projections by design,
// not one generalized type; slice D consumes this one.
type ModelAllowlistCache struct {
	in     chan update
	snap   atomicSnapshot
	cancel context.CancelFunc
	done   chan struct{}
}

type atomicSnapshot struct {
	mu sync.RWMutex
	s  Snapshot
}

func (a *atomicSnapshot) load() Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.s
}

func (a *atomicSnapshot) store(s Snapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.s = s
}

// NewModelAllowlistCache starts the single-writer loop.
func NewModelAllowlistCache(ctx context.Context) *ModelAllowlistCache {
	ctx, cancel := context.WithCancel(ctx)
	c := &ModelAllowlistCache{
		in:     make(chan update),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	c.snap.store(Snapshot{Rev: 0, V: map[string][]string{}})
	go c.loop(ctx)
	return c
}

func (c *ModelAllowlistCache) loop(ctx context.Context) {
	defer close(c.done)
	state := map[string][]string{}
	var rev Revision
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-c.in:
			if !ok {
				return
			}
			if u.rev > rev {
				state[u.subject] = append([]string(nil), u.models...)
				rev = u.rev
				c.snap.store(Snapshot{Rev: rev, V: cloneMap(state)})
			}
			close(u.ack) // stale or not, the write is settled
		}
	}
}

// Upsert submits a projection update and returns once the writer has
// settled it — accepted if the revision is monotonic, suppressed as
// stale otherwise. Blocking makes the revision outcome observable.
func (c *ModelAllowlistCache) Upsert(rev Revision, subject string, models []string) {
	ack := make(chan struct{})
	c.in <- update{rev: rev, subject: subject, models: models, ack: ack}
	<-ack
}

// Get returns the latest snapshot for reading.
func (c *ModelAllowlistCache) Get() Snapshot { return c.snap.load() }

// Close stops the writer loop.
func (c *ModelAllowlistCache) Close() {
	c.cancel()
	<-c.done
}

func cloneMap(m map[string][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = append([]string(nil), v...)
	}
	return out
}
