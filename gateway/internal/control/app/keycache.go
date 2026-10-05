// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"sync"
	"time"
)

// KeyEntry is one projected key: the owning principal, the wire status
// the feed reported, and when the cache last wrote it. SyncedAt feeds
// both the authenticator's positive-TTL decision and the operator's
// freshness view.
type KeyEntry struct {
	PrincipalID string
	Status      string
	SyncedAt    time.Time
}

// KeySnapshot is a cache view: clone-on-store isolates the single
// writer from readers, but recipients MUST NOT mutate V — Go cannot
// enforce immutability, so the contract is documented and reviewed
// (the ModelAllowlistCache precedent).
type KeySnapshot struct {
	Rev Revision
	V   map[[32]byte]KeyEntry
}

type keyUpdate struct {
	rev     Revision
	upserts map[[32]byte]KeyEntry
	deletes [][32]byte
	force   bool // Reset path: authority restart, wholesale replace
	confirm bool // origin confirm: no revision, applied unconditionally
	ack     chan struct{}
}

// KeyCache caches keyHash → principal projections from the identity
// feed. Single-writer event loop (component-patterns #2): one
// goroutine owns the map; readers take immutable snapshots. No locks
// across I/O; stale (non-monotonic) revisions are dropped — except an
// explicit Reset, which only the syncer issues after proving an
// authority restart (a response rev below the last applied rev).
//
// The single writer is the Syncer. The IdentityAuthenticator never
// writes here: its origin confirms ride the separate Confirm method,
// which carries no revision and is applied unconditionally (new
// information, never stale). A syncer delete therefore always wins
// over a confirm for the same hash — fail closed, the next request
// revalidates at the origin.
type KeyCache struct {
	in     chan keyUpdate
	snap   atomicKeySnapshot
	cancel context.CancelFunc
	done   chan struct{}
	clock  func() time.Time
}

type atomicKeySnapshot struct {
	mu sync.RWMutex
	s  KeySnapshot
}

func (a *atomicKeySnapshot) load() KeySnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.s
}

func (a *atomicKeySnapshot) store(s KeySnapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.s = s
}

// NewKeyCache starts the single-writer loop; clock stamps Confirm
// entries (injected per AD-25).
func NewKeyCache(clock func() time.Time) *KeyCache {
	ctx, cancel := context.WithCancel(context.Background())
	c := &KeyCache{
		in:     make(chan keyUpdate),
		cancel: cancel,
		done:   make(chan struct{}),
		clock:  clock,
	}
	c.snap.store(KeySnapshot{Rev: 0, V: map[[32]byte]KeyEntry{}})
	go c.loop(ctx)
	return c
}

func (c *KeyCache) loop(ctx context.Context) {
	defer close(c.done)
	state := map[[32]byte]KeyEntry{}
	var rev Revision
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-c.in:
			if !ok {
				return
			}
			switch {
			case u.force:
				// Authority restart proven by the syncer: the snapshot
				// is the complete current truth, omissions included.
				state = cloneKeyMap(u.upserts)
				rev = u.rev
				c.snap.store(KeySnapshot{Rev: rev, V: cloneKeyMap(state)})
			case u.confirm:
				// Origin confirmation: new information, never stale;
				// the projection revision does not move.
				for _, h := range u.deletes {
					delete(state, h)
				}
				for h, e := range u.upserts {
					state[h] = e
				}
				c.snap.store(KeySnapshot{Rev: rev, V: cloneKeyMap(state)})
			case u.rev > rev:
				for _, h := range u.deletes {
					delete(state, h)
				}
				for h, e := range u.upserts {
					state[h] = e
				}
				rev = u.rev
				c.snap.store(KeySnapshot{Rev: rev, V: cloneKeyMap(state)})
			}
			close(u.ack) // stale or not, the write is settled
		}
	}
}

// Apply submits a syncer delta (upserts and deletes at one revision)
// and returns once the writer has settled it — accepted when the
// revision is monotonic, suppressed as stale otherwise. Blocking makes
// the revision outcome observable.
func (c *KeyCache) Apply(rev Revision, upserts map[[32]byte]KeyEntry, deletes [][32]byte) {
	ack := make(chan struct{})
	c.in <- keyUpdate{rev: rev, upserts: upserts, deletes: deletes, ack: ack}
	<-ack
}

// Reset replaces the projection wholesale, ignoring the monotonic
// guard. Only the syncer calls this, and only after an authority
// restart is provable (response rev below the last applied rev): the
// new snapshot is the complete current truth, and any key it omits is
// gone (fail closed).
func (c *KeyCache) Reset(rev Revision, entries map[[32]byte]KeyEntry) {
	ack := make(chan struct{})
	c.in <- keyUpdate{rev: rev, upserts: entries, force: true, ack: ack}
	<-ack
}

// Confirm records an origin-validated active key. It carries no
// revision (the origin knows nothing of the feed's rev counter), so
// the writer applies it unconditionally and keeps the projection rev
// untouched. Syncer deletes still win over confirms — fail closed.
func (c *KeyCache) Confirm(hash [32]byte, principalID string) {
	ack := make(chan struct{})
	c.in <- keyUpdate{
		confirm: true,
		upserts: map[[32]byte]KeyEntry{hash: {PrincipalID: principalID, Status: "active", SyncedAt: c.clock()}},
		ack:     ack,
	}
	<-ack
}

// Get returns the latest snapshot for reading.
func (c *KeyCache) Get() KeySnapshot { return c.snap.load() }

// Close stops the writer loop.
func (c *KeyCache) Close() {
	c.cancel()
	<-c.done
}

func cloneKeyMap(m map[[32]byte]KeyEntry) map[[32]byte]KeyEntry {
	out := make(map[[32]byte]KeyEntry, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
