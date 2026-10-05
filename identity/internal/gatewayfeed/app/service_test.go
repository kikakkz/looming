// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	feeddomain "github.com/kikakkz/looming/identity/internal/gatewayfeed/domain"
	feedport "github.com/kikakkz/looming/identity/internal/gatewayfeed/port"
)

// --- test doubles ---

type fakeStore struct {
	keys       []feeddomain.Key
	principals []feeddomain.Principal
	byHash     map[string]fakeLookup
	err        error
}

// fakeLookup pairs the key row with its principal's status — the join
// the real adapter performs in SQL.
type fakeLookup struct {
	key             feeddomain.Key
	principalStatus string
}

func (f *fakeStore) ListKeys(context.Context) ([]feeddomain.Key, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.keys, nil
}

func (f *fakeStore) ListPrincipals(context.Context) ([]feeddomain.Principal, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.principals, nil
}

func (f *fakeStore) ByHash(_ context.Context, hash []byte) (feeddomain.Key, string, error) {
	if f.err != nil {
		return feeddomain.Key{}, "", f.err
	}
	lookup, ok := f.byHash[string(hash)]
	if !ok {
		return feeddomain.Key{}, "", feeddomain.ErrNotFound
	}
	return lookup.key, lookup.principalStatus, nil
}

func newTestService(store feedport.Store, watchTimeout time.Duration) *Service {
	return NewService(store, NewHub(), watchTimeout)
}

var feedKeys = []feeddomain.Key{
	{Hash: []byte("hash-one"), PrincipalID: "p-1", Status: feeddomain.KeyActive},
	{Hash: []byte("hash-two"), PrincipalID: "p-1", Status: feeddomain.KeyRevoked},
}

func TestSnapshotReturnsFullProjectionWithCurrentRev(t *testing.T) {
	store := &fakeStore{keys: feedKeys, principals: []feeddomain.Principal{{ID: "p-1", Status: feeddomain.PrincipalActive}}}
	svc := newTestService(store, time.Second)
	svc.hub.Bump()
	svc.hub.Bump()

	snap, err := svc.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Rev != 2 {
		t.Fatalf("snapshot rev must be the hub's current rev, got %d", snap.Rev)
	}
	if diff := cmp.Diff(feedKeys, snap.Keys, cmp.AllowUnexported(feeddomain.Key{})); diff != "" {
		t.Fatalf("keys mismatch (-want +got):\n%s", diff)
	}
	if len(snap.Principals) != 1 || snap.Principals[0].ID != "p-1" {
		t.Fatalf("principals mismatch: %+v", snap.Principals)
	}
}

func TestWatchReturnsImmediatelyWhenRevAdvanced(t *testing.T) {
	store := &fakeStore{keys: feedKeys}
	svc := newTestService(store, time.Second)
	svc.hub.Bump()

	start := time.Now()
	snap, err := svc.Watch(context.Background(), 0)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if snap.Rev != 1 {
		t.Fatalf("want rev 1, got %d", snap.Rev)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("an advanced rev must return immediately, took %v", elapsed)
	}
}

func TestWatchHoldsUntilBump(t *testing.T) {
	store := &fakeStore{keys: feedKeys}
	svc := newTestService(store, 5*time.Second)
	// No bumps: rev 0 == since 0 → the watch must hold.
	done := make(chan *feeddomain.Snapshot, 1)
	go func() {
		snap, _ := svc.Watch(context.Background(), 0)
		done <- snap
	}()

	// The watch must not return before a bump.
	select {
	case <-done:
		t.Fatal("watch returned before any revision bump")
	case <-time.After(150 * time.Millisecond):
	}

	svc.hub.Bump()
	select {
	case snap := <-done:
		if snap.Rev != 1 {
			t.Fatalf("post-bump snapshot rev = %d, want 1", snap.Rev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch must return promptly after a bump")
	}
}

func TestWatchTimesOutWithCurrentSnapshot(t *testing.T) {
	store := &fakeStore{keys: feedKeys}
	svc := newTestService(store, 100*time.Millisecond)

	start := time.Now()
	snap, err := svc.Watch(context.Background(), 0)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("an unbumped watch must hold for the timeout, released after %v", elapsed)
	}
	if snap.Rev != 0 {
		t.Fatalf("timeout returns the current snapshot regardless, got rev %d", snap.Rev)
	}
	if len(snap.Keys) != len(feedKeys) {
		t.Fatalf("timeout snapshot must still carry the projection, got %+v", snap.Keys)
	}
}

func TestWatchRespectsCancellation(t *testing.T) {
	store := &fakeStore{keys: feedKeys}
	svc := newTestService(store, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := svc.Watch(ctx, 0)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a cancelled watch must return the snapshot, not an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation must release the watch")
	}
}

func TestWatchConcurrentWaitersAllWake(t *testing.T) {
	store := &fakeStore{keys: feedKeys}
	svc := newTestService(store, 5*time.Second)
	const waiters = 8
	var wg sync.WaitGroup
	wg.Add(waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			defer wg.Done()
			snap, err := svc.Watch(context.Background(), 0)
			if err != nil || snap.Rev != 1 {
				t.Errorf("waiter got rev %d, err %v", snap.Rev, err)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	svc.hub.Bump()
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("every waiter must wake on a single bump")
	}
}

func TestValidateActiveKey(t *testing.T) {
	hash := []byte("hash-active")
	store := &fakeStore{byHash: map[string]fakeLookup{
		string(hash): {key: feeddomain.Key{Hash: hash, PrincipalID: "p-1", Status: feeddomain.KeyActive}, principalStatus: feeddomain.PrincipalActive},
	}}
	svc := newTestService(store, time.Second)
	principalID, err := svc.Validate(context.Background(), hash)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if principalID != "p-1" {
		t.Fatalf("principal mismatch: %q", principalID)
	}
}

func TestValidateFailsClosed(t *testing.T) {
	revoked := []byte("hash-revoked")
	disabledOwner := []byte("hash-disabled-owner")
	pendingOwner := []byte("hash-pending-owner")
	store := &fakeStore{byHash: map[string]fakeLookup{
		string(revoked):       {key: feeddomain.Key{Hash: revoked, PrincipalID: "p-1", Status: feeddomain.KeyRevoked}, principalStatus: feeddomain.PrincipalActive},
		string(disabledOwner): {key: feeddomain.Key{Hash: disabledOwner, PrincipalID: "p-2", Status: feeddomain.KeyActive}, principalStatus: feeddomain.PrincipalDisabled},
		string(pendingOwner):  {key: feeddomain.Key{Hash: pendingOwner, PrincipalID: "p-3", Status: feeddomain.KeyActive}, principalStatus: feeddomain.PrincipalPending},
	}}
	svc := newTestService(store, time.Second)

	if _, err := svc.Validate(context.Background(), revoked); err != feeddomain.ErrNotFound {
		t.Fatalf("revoked key must 404, got %v", err)
	}
	if _, err := svc.Validate(context.Background(), disabledOwner); err != feeddomain.ErrNotFound {
		t.Fatalf("a disabled principal's key must 404, got %v", err)
	}
	if _, err := svc.Validate(context.Background(), pendingOwner); err != feeddomain.ErrNotFound {
		t.Fatalf("a pending principal's key must 404, got %v", err)
	}
	if _, err := svc.Validate(context.Background(), []byte("never-issued")); err != feeddomain.ErrNotFound {
		t.Fatalf("unknown key must 404, got %v", err)
	}
}

func TestSnapshotSurfacesStoreFailure(t *testing.T) {
	store := &fakeStore{err: context.DeadlineExceeded}
	svc := newTestService(store, time.Second)
	if _, err := svc.Snapshot(context.Background()); err == nil {
		t.Fatal("a store failure must surface")
	}
	if _, err := svc.Watch(context.Background(), 0); err == nil {
		t.Fatal("a store failure must surface even on the immediate path")
	}
}

// TestValidateSurfacesStoreFailure exercises the validate error path.
func TestValidateSurfacesStoreFailure(t *testing.T) {
	store := &fakeStore{err: context.DeadlineExceeded}
	svc := newTestService(store, time.Second)
	if _, err := svc.Validate(context.Background(), []byte("x")); err == nil {
		t.Fatal("a store failure must surface")
	}
}

// TestWatchHandlesMissingStoreKeysCoversEmptySnapshot guards the empty
// projection path (a fresh deployment has nothing to sync yet).
func TestWatchHandlesEmptySnapshot(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store, 50*time.Millisecond)
	snap, err := svc.Watch(context.Background(), 0)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if len(snap.Keys) != 0 || len(snap.Principals) != 0 {
		t.Fatalf("empty projection expected, got %+v", snap)
	}
}
