// SPDX-License-Identifier: Apache-2.0
package app

import (
	"sync"
	"testing"
	"time"
)

var cacheNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func hashOf(b byte) [32]byte {
	var h [32]byte
	for i := range h {
		h[i] = b
	}
	return h
}

func TestKeyCacheAppliesMonotonicRevisions(t *testing.T) {
	c := NewKeyCache(func() time.Time { return cacheNow })
	defer c.Close()
	c.Apply(1, map[[32]byte]KeyEntry{hashOf(1): {PrincipalID: "p-1", Status: "active"}}, nil)
	snap := c.Get()
	if snap.Rev != 1 || snap.V[hashOf(1)].PrincipalID != "p-1" {
		t.Fatalf("want rev 1 with p-1's entry, got %+v", snap)
	}
}

func TestKeyCacheDropsStaleRevisions(t *testing.T) {
	c := NewKeyCache(func() time.Time { return cacheNow })
	defer c.Close()
	c.Apply(5, map[[32]byte]KeyEntry{hashOf(1): {PrincipalID: "p-1", Status: "active"}}, nil)
	c.Apply(3, map[[32]byte]KeyEntry{hashOf(2): {PrincipalID: "p-2", Status: "active"}}, nil) // stale
	snap := c.Get()
	if snap.Rev != 5 || len(snap.V) != 1 {
		t.Fatalf("stale apply must be suppressed, got %+v", snap)
	}
	if _, leaked := snap.V[hashOf(2)]; leaked {
		t.Fatal("stale apply leaked an entry")
	}
}

func TestKeyCacheDeleteAndReset(t *testing.T) {
	c := NewKeyCache(func() time.Time { return cacheNow })
	defer c.Close()
	c.Apply(1, map[[32]byte]KeyEntry{
		hashOf(1): {PrincipalID: "p-1", Status: "active"},
		hashOf(2): {PrincipalID: "p-2", Status: "active"},
	}, nil)
	c.Apply(2, nil, [][32]byte{hashOf(1)})
	snap := c.Get()
	if _, ok := snap.V[hashOf(1)]; ok {
		t.Fatal("delete must remove the entry")
	}
	if _, ok := snap.V[hashOf(2)]; !ok {
		t.Fatal("untouched entries must survive a delete")
	}

	// Reset is the authority-restart path: unconditional, and it drops
	// entries the new snapshot does not carry (fail closed on rollback).
	c.Apply(3, map[[32]byte]KeyEntry{hashOf(3): {PrincipalID: "p-3", Status: "active"}}, nil)
	c.Reset(1, map[[32]byte]KeyEntry{hashOf(9): {PrincipalID: "p-9", Status: "active"}})
	snap = c.Get()
	if snap.Rev != 1 || len(snap.V) != 1 || snap.V[hashOf(9)].PrincipalID != "p-9" {
		t.Fatalf("reset must replace the projection wholesale, got %+v", snap)
	}
}

func TestKeyCacheConfirmRefreshesSyncedAt(t *testing.T) {
	now := cacheNow
	c := NewKeyCache(func() time.Time { return now })
	defer c.Close()
	c.Apply(1, map[[32]byte]KeyEntry{hashOf(1): {PrincipalID: "p-1", Status: "active", SyncedAt: now}}, nil)

	// A syncer delete racing a fresh origin confirm resolves in the
	// delete's favor — fail closed, the next request revalidates.
	c.Confirm(hashOf(1), "p-1")
	now = now.Add(time.Minute)
	c.Confirm(hashOf(1), "p-1")
	snap := c.Get()
	entry, ok := snap.V[hashOf(1)]
	if !ok || entry.PrincipalID != "p-1" {
		t.Fatalf("confirm must upsert the entry, got %+v", snap.V)
	}
	if !entry.SyncedAt.Equal(now) {
		t.Fatalf("confirm must stamp the injected clock, got %v", entry.SyncedAt)
	}
	if !entry.Confirmed {
		t.Fatal("confirm must mark the entry as origin-validated")
	}
	if snap.Rev != 1 {
		t.Fatalf("confirm must not move the projection revision, got %d", snap.Rev)
	}
}

func TestKeyCacheWriterIsolatedFromMutatingReaders(t *testing.T) {
	c := NewKeyCache(func() time.Time { return cacheNow })
	defer c.Close()
	c.Apply(1, map[[32]byte]KeyEntry{hashOf(1): {PrincipalID: "p-1", Status: "active"}}, nil)
	snap := c.Get()
	snap.V[hashOf(1)] = KeyEntry{PrincipalID: "mutated", Status: "active"}
	c.Apply(2, map[[32]byte]KeyEntry{hashOf(1): {PrincipalID: "p-1", Status: "active"}}, nil)
	fresh := c.Get()
	if fresh.V[hashOf(1)].PrincipalID != "p-1" {
		t.Fatalf("reader mutation corrupted the writer: %+v", fresh)
	}
}

func TestKeyCacheConcurrentReadersAndWriters(t *testing.T) {
	c := NewKeyCache(func() time.Time { return cacheNow })
	defer c.Close()
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 1; i <= 50; i++ {
				//nolint:gosec // test-only revision counter; the loop bounds keep w*50+i far below any overflow.
				rev := uint64(w)*50 + uint64(i)
				c.Apply(Revision(rev), map[[32]byte]KeyEntry{hashOf(byte(i)): {PrincipalID: "p", Status: "active"}}, nil)
			}
		}(w)
	}
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = c.Get()
			}
		}()
	}
	wg.Wait()
	snap := c.Get()
	if snap.Rev != 200 || len(snap.V) != 50 {
		t.Fatalf("racy state mismatch: rev %d entries %d", snap.Rev, len(snap.V))
	}
}

func TestKeyCacheCloseIsIdempotent(t *testing.T) {
	c := NewKeyCache(func() time.Time { return cacheNow })
	c.Close()
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("double Close must not deadlock")
	}
}

// TestKeyCacheConfirmPreservesFeedCredential pins the slice-C merge
// rule: the feed is the credential authority, the origin confirm only
// carries (principal, active) — confirming over a feed-populated entry
// must refresh the confirmation fields without wiping its credential.
func TestKeyCacheConfirmPreservesFeedCredential(t *testing.T) {
	now := cacheNow
	c := NewKeyCache(func() time.Time { return now })
	defer c.Close()
	c.Apply(1, map[[32]byte]KeyEntry{hashOf(1): {PrincipalID: "p-1", Status: "active", EngineCredential: "cred-A", SyncedAt: now}}, nil)

	// The syncer stalls, the entry goes stale, the origin confirms the
	// key — the confirm wins freshness but keeps the feed's credential.
	c.Confirm(hashOf(1), "p-1")
	snap := c.Get()
	entry, ok := snap.V[hashOf(1)]
	if !ok || !entry.Confirmed {
		t.Fatalf("confirm must upsert the confirmed entry, got %+v", snap.V)
	}
	if entry.EngineCredential != "cred-A" {
		t.Fatalf("confirm must preserve the feed-authoritative credential, got %q", entry.EngineCredential)
	}
	if snap.Rev != 1 {
		t.Fatalf("confirm must not move the projection revision, got %d", snap.Rev)
	}
}

// TestKeyCacheConfirmWithoutFeedEntryHasNoCredential pins the other
// half: a confirm for a hash the feed never populated (origin knows the
// key, the projection lags) stores an entry with an empty credential —
// the engine call falls back, and the next feed sync fills the value.
func TestKeyCacheConfirmWithoutFeedEntryHasNoCredential(t *testing.T) {
	c := NewKeyCache(func() time.Time { return cacheNow })
	defer c.Close()
	c.Confirm(hashOf(7), "p-7")
	entry, ok := c.Get().V[hashOf(7)]
	if !ok || entry.PrincipalID != "p-7" || !entry.Confirmed {
		t.Fatalf("confirm must upsert the entry, got %+v", c.Get().V)
	}
	if entry.EngineCredential != "" {
		t.Fatalf("a confirm without feed context must carry no credential, got %q", entry.EngineCredential)
	}
}
