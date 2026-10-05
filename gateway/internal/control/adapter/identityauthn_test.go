// SPDX-License-Identifier: Apache-2.0
package adapter

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/kikakkz/looming/gateway/internal/control/app"
	frontapp "github.com/kikakkz/looming/gateway/internal/front/app"
	frontdomain "github.com/kikakkz/looming/gateway/internal/front/domain"
)

var authnNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

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

// stubOrigin counts validate calls so cache behavior is observable.
type stubOrigin struct {
	calls       int
	mu          sync.Mutex
	principalID string
	status      string
	err         error
}

func (s *stubOrigin) Validate(_ context.Context, _ string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.principalID, s.status, s.err
}

func (s *stubOrigin) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// healthStub is the syncer health probe under test control.
type healthStub struct {
	mu sync.Mutex
	ok bool
}

func (h *healthStub) Healthy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ok
}

func (h *healthStub) Set(ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ok = ok
}

func newAuthnTestRig(t *testing.T) (*app.KeyCache, *stubOrigin, *IdentityAuthenticator, *fakeClock, *healthStub) {
	t.Helper()
	clock := &fakeClock{t: authnNow}
	cache := app.NewKeyCache(clock.Now)
	t.Cleanup(cache.Close)
	origin := &stubOrigin{principalID: "p-origin", status: "active"}
	health := &healthStub{ok: true}
	authn := NewIdentityAuthenticator(cache, origin, 30*time.Second, clock.Now, nil, health.Healthy)
	return cache, origin, authn, clock, health
}

func TestAuthnCacheHitAvoidsOrigin(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, _, _ := newAuthnTestRig(t)
	defer cache.Close()
	raw := "lk-cached"
	hash := sha256.Sum256([]byte(raw))
	cache.Apply(1, map[[32]byte]app.KeyEntry{hash: {PrincipalID: "p-synced", Status: "active", SyncedAt: authnNow}}, nil)

	subject, err := authn.Authenticate(context.Background(), raw)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if subject != "p-synced" {
		t.Fatalf("cache hit must authorize the synced principal, got %q", subject)
	}
	if origin.callCount() != 0 {
		t.Fatalf("a fresh cache hit must not call the origin, made %d calls", origin.callCount())
	}
}

func TestAuthnMissConfirmsPositiveWithTTL(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, clock, _ := newAuthnTestRig(t)
	defer cache.Close()
	raw := "lk-origin"

	subject, err := authn.Authenticate(context.Background(), raw)
	if err != nil || subject != "p-origin" {
		t.Fatalf("origin fallback: got %q (%v)", subject, err)
	}
	if origin.callCount() != 1 {
		t.Fatalf("one origin call expected, got %d", origin.callCount())
	}

	// The positive confirm must satisfy the second call within the TTL
	// without another origin round-trip.
	subject, err = authn.Authenticate(context.Background(), raw)
	if err != nil || subject != "p-origin" {
		t.Fatalf("confirmed cache: got %q (%v)", subject, err)
	}
	if origin.callCount() != 1 {
		t.Fatalf("the confirm must cache the positive for the TTL, got %d origin calls", origin.callCount())
	}

	// Past the TTL the entry revalidates at the origin.
	clock.Advance(31 * time.Second)
	if _, err := authn.Authenticate(context.Background(), raw); err != nil {
		t.Fatalf("revalidation after TTL: %v", err)
	}
	if origin.callCount() != 2 {
		t.Fatalf("TTL expiry must revalidate exactly once, got %d", origin.callCount())
	}
}

func TestAuthnRevokedAfterSyncFailsClosed(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, _, _ := newAuthnTestRig(t)
	defer cache.Close()
	raw := "lk-revoked"
	hash := sha256.Sum256([]byte(raw))
	cache.Apply(1, map[[32]byte]app.KeyEntry{hash: {PrincipalID: "p-synced", Status: "active", SyncedAt: authnNow}}, nil)

	// The syncer drops the revoked key: the projection no longer knows
	// it, the origin 404s it, and the request fails closed.
	cache.Apply(2, nil, [][32]byte{hash})
	origin.err = ErrKeyUnknown
	if _, err := authn.Authenticate(context.Background(), raw); err == nil {
		t.Fatal("a revoked-after-sync key must fail closed")
	}
	if origin.callCount() != 1 {
		t.Fatalf("the miss must reach the origin exactly once, got %d", origin.callCount())
	}
}

func TestAuthnFeedEntryAuthorizesWhileSyncerHealthy(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, clock, health := newAuthnTestRig(t)
	defer cache.Close()
	raw := "lk-feed"
	hash := sha256.Sum256([]byte(raw))
	// A feed entry whose SyncedAt is far older than the TTL still
	// authorizes while the projection is being maintained — presence
	// is its proof, the feed delete is its revocation path.
	cache.Apply(1, map[[32]byte]app.KeyEntry{hash: {PrincipalID: "p-feed", Status: "active", SyncedAt: clock.Now().Add(-time.Hour)}}, nil)
	clock.Advance(time.Hour)
	if !health.Healthy() {
		health.Set(true)
	}
	subject, err := authn.Authenticate(context.Background(), raw)
	if err != nil || subject != "p-feed" {
		t.Fatalf("healthy projection entry: got %q (%v)", subject, err)
	}
	if origin.callCount() != 0 {
		t.Fatalf("a healthy feed entry must not call the origin, got %d", origin.callCount())
	}
}

func TestAuthnStalledSyncerTurnsFeedEntryIntoMiss(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, clock, health := newAuthnTestRig(t)
	defer cache.Close()
	raw := "lk-stalled"
	hash := sha256.Sum256([]byte(raw))
	cache.Apply(1, map[[32]byte]app.KeyEntry{hash: {PrincipalID: "p-feed", Status: "active", SyncedAt: clock.Now().Add(-time.Hour)}}, nil)
	clock.Advance(time.Hour)
	health.Set(false) // projection may be stale — presence is not proof

	subject, err := authn.Authenticate(context.Background(), raw)
	if err != nil || subject != "p-origin" {
		t.Fatalf("stalled projection must revalidate at the origin: got %q (%v)", subject, err)
	}
	if origin.callCount() != 1 {
		t.Fatalf("one origin revalidation expected, got %d", origin.callCount())
	}
}

func TestAuthnOriginDownFailsClosed(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, _, _ := newAuthnTestRig(t)
	defer cache.Close()
	origin.err = errors.New("connection refused")
	if _, err := authn.Authenticate(context.Background(), "lk-anything"); err == nil {
		t.Fatal("an origin outage must fail closed, never authorize")
	}
}

func TestAuthnNegativeResultsAreNotCached(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, _, _ := newAuthnTestRig(t)
	defer cache.Close()
	origin.err = ErrKeyUnknown
	raw := "lk-neg"
	if _, err := authn.Authenticate(context.Background(), raw); err == nil {
		t.Fatal("unknown key must fail")
	}
	// No negative caching: the next request revalidates at the origin,
	// and a now-active answer would succeed (here: same failure).
	if _, err := authn.Authenticate(context.Background(), raw); err == nil {
		t.Fatal("unknown key must keep failing")
	}
	if origin.callCount() != 2 {
		t.Fatalf("negatives must not be cached (no short-TTL negative cache by design), got %d origin calls", origin.callCount())
	}
	if snap := cache.Get(); len(snap.V) != 0 {
		t.Fatalf("a negative result must leave no cache entry, got %+v", snap.V)
	}
}

func TestAuthnNonActiveOriginStatusFailsClosed(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, _, _ := newAuthnTestRig(t)
	defer cache.Close()
	origin.status = "revoked"
	if _, err := authn.Authenticate(context.Background(), "lk-weird"); err == nil {
		t.Fatal("a non-active origin status must fail closed")
	}
}

// --- end-to-end: Front with the identity-backed authenticator ---

type stubAllowlist struct{}

func (stubAllowlist) Models(context.Context, string) ([]string, error) {
	return []string{"gpt-5"}, nil
}

type stubEngine struct{}

func (stubEngine) Forward(_ context.Context, w http.ResponseWriter, _ *http.Request) error {
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte("ok"))
	return err
}

func TestFrontWithIdentityAuthnEndToEnd(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, _, _ := newAuthnTestRig(t)
	defer cache.Close()
	front := frontapp.NewFront(
		authn,
		stubAllowlist{},
		frontdomain.NewChain(),
		stubEngine{},
		headerModelExtractor{},
		nil,
	)

	// 401 path: unknown key, origin 404s it.
	origin.err = ErrKeyUnknown
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer lk-nope")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown key: want 401, got %d", rec.Code)
	}

	// 200 path: confirm a key through the cache, then authorize.
	raw := "lk-good"
	hash := sha256.Sum256([]byte(raw))
	cache.Apply(1, map[[32]byte]app.KeyEntry{hash: {PrincipalID: "p-1", Status: "active", SyncedAt: authnNow}}, nil)
	origin.err = nil
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("X-Test-Model", "gpt-5")
	rec = httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("synced key: want 200 ok, got %d %q", rec.Code, rec.Body.String())
	}
}

type headerModelExtractor struct{}

func (headerModelExtractor) Extract(r *http.Request) (string, error) {
	return r.Header.Get("X-Test-Model"), nil
}
