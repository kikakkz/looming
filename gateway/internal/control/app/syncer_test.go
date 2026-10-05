// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/kikakkz/looming/gateway/internal/control/domain"
)

// fakeIdentity is a scripted identity service: it serves the feed in
// the real wire shape (base64 hashes, rev counter, watch long-poll)
// and lets each test move the state forward, fail requests, or roll
// the revision back (restart).
type fakeIdentity struct {
	mu     sync.Mutex
	rev    uint64
	keys   map[string]string // base64 hash -> principalID
	revoke map[string]bool   // base64 hash -> revoked marker
	ch     chan struct{}     // closed on every state change
	srv    *httptest.Server

	failures int // next N feed requests answer 500
	requests int // total feed requests served (observability)
}

func newFakeIdentity(t *testing.T) *fakeIdentity {
	t.Helper()
	f := &fakeIdentity{keys: map[string]string{}, revoke: map[string]bool{}, ch: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/gateway/feed", f.handleFeed)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdentity) url() string { return f.srv.URL }

func (f *fakeIdentity) setState(rev uint64, keys map[string]string, revoked []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev = rev
	f.keys = keys
	f.revoke = map[string]bool{}
	for _, h := range revoked {
		f.revoke[h] = true
	}
	close(f.ch)
	f.ch = make(chan struct{})
}

func (f *fakeIdentity) failNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = n
}

func (f *fakeIdentity) handleFeed(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	if f.failures > 0 {
		f.failures--
		f.mu.Unlock()
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	since, _ := parseRev(r.URL.Query().Get("since_rev"))
	watch := r.URL.Query().Get("watch") == "1"
	ch := f.ch
	rev := f.rev
	if watch && rev <= since {
		f.mu.Unlock()
		// Hold like the real watch until a state change or the short
		// test timeout, then answer with the current state.
		select {
		case <-ch:
		case <-time.After(60 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		f.mu.Lock()
		rev = f.rev
	}
	keys := make([]map[string]any, 0, len(f.keys))
	for hash, principalID := range f.keys {
		status := "active"
		if f.revoke[hash] {
			status = "revoked"
		}
		keys = append(keys, map[string]any{"hash": hash, "principal_id": principalID, "status": status})
	}
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"rev":        rev,
		"keys":       keys,
		"principals": []any{},
	})
}

func parseRev(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	var n uint64
	_, err := fmt.Sscanf(raw, "%d", &n)
	return n, err
}

// feedClientFor tests the syncer through the real HTTP seam? No — the
// syncer's seam is FeedSource; these tests feed it a source backed by
// the fake identity via the adapter in adapter tests. Here a tiny
// source over the fake keeps the suite focused on syncer semantics.

type httpFeedSource struct {
	base string
}

func (h httpFeedSource) do(ctx context.Context, query string) (FeedResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+"/v1/gateway/feed?"+query, nil)
	if err != nil {
		return FeedResponse{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return FeedResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return FeedResponse{}, fmt.Errorf("feed status %d", resp.StatusCode)
	}
	var body struct {
		Rev  uint64 `json:"rev"`
		Keys []struct {
			Hash        string `json:"hash"`
			PrincipalID string `json:"principal_id"`
			Status      string `json:"status"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return FeedResponse{}, err
	}
	out := FeedResponse{Rev: body.Rev, Keys: make([]FeedKey, 0, len(body.Keys))}
	for _, k := range body.Keys {
		raw, err := base64.StdEncoding.DecodeString(k.Hash)
		if err != nil || len(raw) != 32 {
			return FeedResponse{}, errors.New("bad hash in feed")
		}
		var hash [32]byte
		copy(hash[:], raw)
		out.Keys = append(out.Keys, FeedKey{Hash: hash, PrincipalID: k.PrincipalID, Status: k.Status})
	}
	return out, nil
}

func (h httpFeedSource) Snapshot(ctx context.Context) (FeedResponse, error) {
	return h.do(ctx, "")
}

func (h httpFeedSource) Watch(ctx context.Context, sinceRev uint64) (FeedResponse, error) {
	return h.do(ctx, fmt.Sprintf("watch=1&since_rev=%d", sinceRev))
}

var testSyncNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func newTestSyncer(t *testing.T, fake *fakeIdentity) (*Syncer, *KeyCache, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: testSyncNow}
	cache := NewKeyCache(clock.Now)
	t.Cleanup(cache.Close) // backstop; tests close explicitly before goleak
	syncer := NewSyncer(httpFeedSource{base: fake.url()}, cache,
		domain.Backoffer{Base: time.Millisecond, Cap: 5 * time.Millisecond},
		30*time.Second, clock.Now, nil)
	return syncer, cache, clock
}

// fakeClock is an injectable wall clock for the syncer (AD-25).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// waitFor polls pred until it holds or the deadline passes.
func waitFor(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func keyA() (string, [32]byte) {
	var h [32]byte
	h[0] = 'A'
	return base64.StdEncoding.EncodeToString(h[:]), h
}

func keyB() (string, [32]byte) {
	var h [32]byte
	h[0] = 'B'
	return base64.StdEncoding.EncodeToString(h[:]), h
}

func TestSyncerBootsThenAppliesWatchDiffs(t *testing.T) {
	defer goleak.VerifyNone(t)
	fake := newFakeIdentity(t)
	hashA64, hashA := keyA()
	hashB64, hashB := keyB()
	fake.setState(1, map[string]string{hashA64: "p-1"}, nil)

	syncer, cache, _ := newTestSyncer(t, fake)
	defer cache.Close()
	defer fake.srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = syncer.Run(ctx); close(done) }()

	waitFor(t, "boot snapshot applied", func() bool {
		snap := cache.Get()
		e, ok := snap.V[hashA]
		return ok && e.PrincipalID == "p-1" && snap.Rev == 1
	})
	if !syncer.Healthy() {
		t.Fatal("a fresh sync must read healthy")
	}

	// Move the feed forward: A revoked, B issued. The long-poll watch
	// unblocks and the diff lands — A deletes, B upserts.
	fake.setState(2, map[string]string{hashA64: "p-1", hashB64: "p-2"}, []string{hashA64})
	waitFor(t, "watch diff applied", func() bool {
		snap := cache.Get()
		_, aGone := snap.V[hashA]
		b, bOk := snap.V[hashB]
		return !aGone && bOk && b.PrincipalID == "p-2" && snap.Rev == 2
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("syncer must stop on context cancellation")
	}
}

func TestSyncerResetOnRevRollback(t *testing.T) {
	defer goleak.VerifyNone(t)
	fake := newFakeIdentity(t)
	hashA64, hashA := keyA()
	hashB64, hashB := keyB()
	fake.setState(5, map[string]string{hashA64: "p-1"}, nil)

	syncer, cache, _ := newTestSyncer(t, fake)
	defer cache.Close()
	defer fake.srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = syncer.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "pre-restart snapshot", func() bool {
		_, ok := cache.Get().V[hashA]
		return ok
	})

	// Identity restarts: rev resets below the last applied revision.
	fake.setState(1, map[string]string{hashB64: "p-9"}, nil)
	waitFor(t, "projection reset", func() bool {
		snap := cache.Get()
		_, aGone := snap.V[hashA]
		b, bOk := snap.V[hashB]
		return !aGone && bOk && b.PrincipalID == "p-9" && snap.Rev == 1
	})
}

func TestSyncerRetriesOnFailuresAndRecovers(t *testing.T) {
	defer goleak.VerifyNone(t)
	fake := newFakeIdentity(t)
	hashA64, hashA := keyA()
	fake.setState(1, map[string]string{hashA64: "p-1"}, nil)
	fake.failNext(3) // three 500s before the good response

	syncer, cache, _ := newTestSyncer(t, fake)
	defer cache.Close()
	defer fake.srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = syncer.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "recovery after backoff retries", func() bool {
		_, ok := cache.Get().V[hashA]
		return ok
	})
	if got := fakeRequestCount(fake); got < 4 {
		t.Fatalf("the syncer must retry after 500s (>=4 requests), made %d", got)
	}
}

func fakeRequestCount(f *fakeIdentity) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func TestSyncerBootsAtRevZeroWithoutBusyLoop(t *testing.T) {
	defer goleak.VerifyNone(t)
	fake := newFakeIdentity(t)
	hashA64, hashA := keyA()
	// A fresh identity sits at rev 0 until the first mutation — the
	// boot snapshot must land and the loop must hold on the watch,
	// not spin full snapshots.
	fake.setState(0, map[string]string{hashA64: "p-1"}, nil)

	syncer, cache, _ := newTestSyncer(t, fake)
	defer cache.Close()
	defer fake.srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = syncer.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "rev-0 boot snapshot applied", func() bool {
		snap := cache.Get()
		e, ok := snap.V[hashA]
		return ok && e.PrincipalID == "p-1"
	})
	if !syncer.Healthy() {
		t.Fatal("a rev-0 boot must still mark the syncer healthy")
	}

	// The loop must be holding on the watch (bounded request rate),
	// not re-fetching full snapshots in a tight loop.
	time.Sleep(300 * time.Millisecond)
	if got := fakeRequestCount(fake); got > 8 {
		t.Fatalf("rev-0 watch must hold without a busy loop, made %d requests", got)
	}

	// And a mutation from rev 0 still propagates through the watch.
	hashB64, hashB := keyB()
	fake.setState(1, map[string]string{hashA64: "p-1", hashB64: "p-2"}, nil)
	waitFor(t, "post-boot watch diff", func() bool {
		_, ok := cache.Get().V[hashB]
		return ok
	})
}

func TestSyncerHealthyFlipsStale(t *testing.T) {
	defer goleak.VerifyNone(t)
	fake := newFakeIdentity(t)
	hashA64, _ := keyA()
	fake.setState(1, map[string]string{hashA64: "p-1"}, nil)

	syncer, cache, clock := newTestSyncer(t, fake)
	defer cache.Close()
	defer fake.srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = syncer.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "first sync", func() bool { return syncer.Healthy() })
	clock.Advance(2 * 30 * time.Second) // beyond staleAfter (30s)
	if syncer.Healthy() {
		t.Fatal("a projection older than staleAfter must read unhealthy")
	}
}

func TestSyncerConnectionRefusedKeepsRetrying(t *testing.T) {
	defer goleak.VerifyNone(t)
	fake := newFakeIdentity(t)
	syncer, cache, _ := newTestSyncer(t, fake)
	defer cache.Close()
	fake.srv.Close() // everything now refuses

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = syncer.Run(ctx); close(done) }()

	// The syncer must survive a refused origin without applying
	// anything and without dying; cancellation still stops it cleanly.
	time.Sleep(150 * time.Millisecond)
	if syncer.Healthy() {
		t.Fatal("never-synced must read unhealthy")
	}
	if snap := cache.Get(); len(snap.V) != 0 {
		t.Fatalf("a refused origin must not populate the projection, got %+v", snap.V)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("syncer must stop on context cancellation even while retrying")
	}
}
