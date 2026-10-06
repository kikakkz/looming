// SPDX-License-Identifier: Apache-2.0
package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/kikakkz/looming/gateway/internal/control/app"
	defaultengine "github.com/kikakkz/looming/gateway/internal/engine/default"
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

	id, err := authn.Authenticate(context.Background(), raw)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != "p-synced" {
		t.Fatalf("cache hit must authorize the synced principal, got %q", id.Subject)
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

	id, err := authn.Authenticate(context.Background(), raw)
	if err != nil || id.Subject != "p-origin" {
		t.Fatalf("origin fallback: got %q (%v)", id.Subject, err)
	}
	if origin.callCount() != 1 {
		t.Fatalf("one origin call expected, got %d", origin.callCount())
	}

	// The positive confirm must satisfy the second call within the TTL
	// without another origin round-trip.
	id, err = authn.Authenticate(context.Background(), raw)
	if err != nil || id.Subject != "p-origin" {
		t.Fatalf("confirmed cache: got %q (%v)", id.Subject, err)
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
	id, err := authn.Authenticate(context.Background(), raw)
	if err != nil || id.Subject != "p-feed" {
		t.Fatalf("healthy projection entry: got %q (%v)", id.Subject, err)
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

	id, err := authn.Authenticate(context.Background(), raw)
	if err != nil || id.Subject != "p-origin" {
		t.Fatalf("stalled projection must revalidate at the origin: got %q (%v)", id.Subject, err)
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

func (stubEngine) Forward(_ context.Context, w http.ResponseWriter, _ *http.Request, _ string) error {
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

// --- slice C: engine credential through the identity-backed authenticator ---

// TestAuthnCacheHitCarriesEngineCredential pins the hit path: a
// feed-synced entry with a provisioned credential resolves BOTH the
// subject and the credential, without an origin round-trip.
func TestAuthnCacheHitCarriesEngineCredential(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, _, _ := newAuthnTestRig(t)
	defer cache.Close()
	raw := "lk-cred"
	hash := sha256.Sum256([]byte(raw))
	cache.Apply(1, map[[32]byte]app.KeyEntry{hash: {
		PrincipalID: "p-synced", Status: "active", EngineCredential: "cred-synced", SyncedAt: authnNow,
	}}, nil)

	id, err := authn.Authenticate(context.Background(), raw)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != "p-synced" || id.EngineCredential != "cred-synced" {
		t.Fatalf("cache hit must resolve subject AND credential, got %+v", id)
	}
	if origin.callCount() != 0 {
		t.Fatalf("a fresh cache hit must not call the origin, made %d calls", origin.callCount())
	}
}

// TestAuthnOriginFallbackHasEmptyCredential pins the documented
// fallback gap: identity's validate endpoint answers (principal,
// status) only — no credential. A key confirmed at the origin
// authorizes with an EMPTY credential (the engine falls back to its
// static auth), and the feed fills the value on its next sync. This
// keeps the gateway's wire vocabulary the single authority for
// credential material.
func TestAuthnOriginFallbackHasEmptyCredential(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, authn, _, _ := newAuthnTestRig(t)
	defer cache.Close()

	id, err := authn.Authenticate(context.Background(), "lk-origin-only")
	if err != nil {
		t.Fatalf("origin fallback: %v", err)
	}
	if id.Subject != "p-origin" {
		t.Fatalf("want the origin principal, got %q", id.Subject)
	}
	if id.EngineCredential != "" {
		t.Fatalf("the validate fallback carries no credential, got %q", id.EngineCredential)
	}
}

// --- end-to-end: the real default engine against an httptest upstream ---

// upstreamAuthRecorder stands in for the engine provider, recording
// the Authorization header of every request keyed by X-Seq.
type upstreamAuthRecorder struct {
	mu    sync.Mutex
	auths map[string]string
	srv   *httptest.Server
	hits  int
}

func newUpstreamAuthRecorder(t *testing.T) *upstreamAuthRecorder {
	t.Helper()
	a := &upstreamAuthRecorder{auths: map[string]string{}}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.auths[r.Header.Get("X-Seq")] = r.Header.Get("Authorization")
		a.hits++
		a.mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *upstreamAuthRecorder) authFor(seq string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.auths[seq]
}

func (a *upstreamAuthRecorder) hitCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hits
}

// recordingQueue captures emitted interactions for assertions.
type recordingQueue struct {
	mu     sync.Mutex
	bodies []frontdomain.InteractionBody
}

func (q *recordingQueue) Enqueue(_ context.Context, b frontdomain.InteractionBody) {
	q.mu.Lock()
	q.bodies = append(q.bodies, b)
	q.mu.Unlock()
}

// newEngineFrontRig wires the real pipeline pieces: the
// identity-backed authenticator over a real KeyCache and the DEFAULT
// engine proxying to a recording httptest upstream with a static auth
// configured. Tests build their own Front on top (recording or not).
func newEngineFrontRig(t *testing.T, staticAuth string) (*app.KeyCache, *stubOrigin, *upstreamAuthRecorder, *defaultengine.Engine) {
	t.Helper()
	clock := &fakeClock{t: authnNow}
	cache := app.NewKeyCache(clock.Now)
	t.Cleanup(cache.Close)
	origin := &stubOrigin{principalID: "p-origin", status: "active"}

	upstream := newUpstreamAuthRecorder(t)
	u, err := url.Parse(upstream.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	engine := defaultengine.NewWithUpstream(u, staticAuth)
	return cache, origin, upstream, engine
}

// newEngineFront assembles the Front over the rig's real components.
func newEngineFront(authn *IdentityAuthenticator, engine *defaultengine.Engine, opts ...frontapp.FrontOption) *frontapp.Front {
	return frontapp.NewFront(
		authn,
		stubAllowlist{},
		frontdomain.NewChain(),
		engine,
		headerModelExtractor{},
		nil,
		opts...,
	)
}

func engineFrontRequest(rawKey, seq, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("X-Test-Model", "gpt-5")
	req.Header.Set("X-Seq", seq)
	return req
}

// TestFrontEndToEndEngineCredentialInjection is the slice-C scenario
// spine over real components: provisioned key → upstream receives the
// per-key credential; unprovisioned key → the static fallback auth;
// revoked key → 401 before any upstream contact.
func TestFrontEndToEndEngineCredentialInjection(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, upstream, engine := newEngineFrontRig(t, "static-fallback-secret")
	authn := NewIdentityAuthenticator(cache, origin, 30*time.Second, func() time.Time { return authnNow }, nil, nil)
	front := newEngineFront(authn, engine)

	// Provisioned key: the feed-projected credential wins upstream.
	rawProv := "lk-e2e-provisioned"
	hashProv := sha256.Sum256([]byte(rawProv))
	cache.Apply(1, map[[32]byte]app.KeyEntry{hashProv: {
		PrincipalID: "p-1", Status: "active", EngineCredential: "e2e-engine-cred-A", SyncedAt: authnNow,
	}}, nil)
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, engineFrontRequest(rawProv, "prov", `{"model":"gpt-5"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("provisioned key: want 200, got %d", rec.Code)
	}
	if got := upstream.authFor("prov"); got != "Bearer e2e-engine-cred-A" {
		t.Fatalf("provisioned key must inject its credential upstream, got %q", got)
	}

	// Unprovisioned key: empty credential → the static fallback auth.
	rawPlain := "lk-e2e-plain"
	hashPlain := sha256.Sum256([]byte(rawPlain))
	cache.Apply(2, map[[32]byte]app.KeyEntry{hashPlain: {
		PrincipalID: "p-2", Status: "active", SyncedAt: authnNow,
	}}, nil)
	rec = httptest.NewRecorder()
	front.ServeHTTP(rec, engineFrontRequest(rawPlain, "plain", `{"model":"gpt-5"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("unprovisioned key: want 200, got %d", rec.Code)
	}
	if got := upstream.authFor("plain"); got != "Bearer static-fallback-secret" {
		t.Fatalf("unprovisioned key must fall back to the static auth, got %q", got)
	}

	// Revoked key: the projection deletes the row, the origin 404s,
	// the caller sees 401 and the upstream is never touched.
	rawRevoked := "lk-e2e-revoked"
	hashRevoked := sha256.Sum256([]byte(rawRevoked))
	cache.Apply(3, map[[32]byte]app.KeyEntry{hashRevoked: {
		PrincipalID: "p-3", Status: "active", EngineCredential: "e2e-engine-cred-C", SyncedAt: authnNow,
	}}, nil)
	cache.Apply(4, nil, [][32]byte{hashRevoked})
	origin.err = ErrKeyUnknown
	hitsBefore := upstream.hitCount()
	rec = httptest.NewRecorder()
	front.ServeHTTP(rec, engineFrontRequest(rawRevoked, "revoked", `{"model":"gpt-5"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key: want 401, got %d", rec.Code)
	}
	if upstream.hitCount() != hitsBefore {
		t.Fatal("a revoked key must never reach the upstream")
	}
}

// TestFrontEndToEndRecordingNeverCarriesCredentials pins the
// transcript invariant against the REAL engine path: the recorded
// interaction must contain neither the LoomingKey nor the engine
// credential — the records ride bodies only, and both canaries are
// distinct so a bleed either way fails.
func TestFrontEndToEndRecordingNeverCarriesCredentials(t *testing.T) {
	defer goleak.VerifyNone(t)
	const loomKey = "lk-e2e-canary-key"
	const engineCred = "e2e-engine-cred-canary"

	cache, origin, upstream, engine := newEngineFrontRig(t, "static-fallback-secret")
	queue := &recordingQueue{}
	front := newEngineFront(
		NewIdentityAuthenticator(cache, origin, 30*time.Second, func() time.Time { return authnNow }, nil, nil),
		engine,
		frontapp.WithRecording(queue, interactionMeter{}, 1<<20),
	)

	hash := sha256.Sum256([]byte(loomKey))
	cache.Apply(1, map[[32]byte]app.KeyEntry{hash: {
		PrincipalID: "p-canary", Status: "active", EngineCredential: engineCred, SyncedAt: authnNow,
	}}, nil)

	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, engineFrontRequest(loomKey, "canary", `{"model":"gpt-5","prompt":"hello"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := upstream.authFor("canary"); got != "Bearer "+engineCred {
		t.Fatalf("sanity: the upstream must have received the per-key credential, got %q", got)
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.bodies) != 1 {
		t.Fatalf("one interaction must be recorded, got %d", len(queue.bodies))
	}
	recorded, err := json.Marshal(queue.bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(recorded, []byte(loomKey)) {
		t.Fatalf("the recorded transcript carries the LoomingKey: %s", recorded)
	}
	if bytes.Contains(recorded, []byte(engineCred)) {
		t.Fatalf("the recorded transcript carries the engine credential: %s", recorded)
	}
}

// interactionMeter is a no-op meter sink: the recording assertion
// cares about the queue only.
type interactionMeter struct{}

func (interactionMeter) Record(context.Context, frontdomain.MeterRecord) {}

// TestFrontEndToEndConcurrentDistinctKeysNoBleed drives mixed traffic —
// distinct keys with distinct credentials plus static-fallback keys —
// through the SHARED front and SHARED reverse proxy concurrently.
// Under -race this proves the per-request credential never bleeds
// across requests.
func TestFrontEndToEndConcurrentDistinctKeysNoBleed(t *testing.T) {
	defer goleak.VerifyNone(t)
	cache, origin, upstream, engine := newEngineFrontRig(t, "static-fallback-secret")
	authn := NewIdentityAuthenticator(cache, origin, 30*time.Second, func() time.Time { return authnNow }, nil, nil)
	front := newEngineFront(authn, engine)

	const keys = 9
	const perKey = 8
	for i := 0; i < keys; i++ {
		raw := fmt.Sprintf("lk-conc-%d", i)
		hash := sha256.Sum256([]byte(raw))
		entry := app.KeyEntry{PrincipalID: fmt.Sprintf("p-%d", i), Status: "active", SyncedAt: authnNow}
		if i%3 != 0 {
			entry.EngineCredential = fmt.Sprintf("conc-cred-%d", i)
		}
		cache.Apply(app.Revision(i+1), map[[32]byte]app.KeyEntry{hash: entry}, nil)
	}

	var wg sync.WaitGroup
	errs := make(chan error, keys*perKey)
	for i := 0; i < keys; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perKey; j++ {
				seq := fmt.Sprintf("k%d-%d", i, j)
				rec := httptest.NewRecorder()
				front.ServeHTTP(rec, engineFrontRequest(fmt.Sprintf("lk-conc-%d", i), seq, `{"model":"gpt-5"}`))
				if rec.Code != http.StatusOK {
					errs <- fmt.Errorf("seq %s: want 200, got %d", seq, rec.Code)
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	for i := 0; i < keys; i++ {
		for j := 0; j < perKey; j++ {
			seq := fmt.Sprintf("k%d-%d", i, j)
			want := "Bearer static-fallback-secret"
			if i%3 != 0 {
				want = fmt.Sprintf("Bearer conc-cred-%d", i)
			}
			if got := upstream.authFor(seq); got != want {
				t.Fatalf("seq %s: want %q, got %q (cross-request bleed or wrong fallback)", seq, want, got)
			}
		}
	}
}
