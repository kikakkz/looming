// SPDX-License-Identifier: Apache-2.0
package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kikakkz/looming/gateway/internal/control/app"
)

// identityFake scripts identity's service-token API: the feed (with
// watch long-poll) and the validate endpoint. Tests move its state
// forward via setState / issue / issueProvisioned / revoke.
type identityFake struct {
	mu       sync.Mutex
	rev      uint64
	keys     map[string][32]byte // raw key -> hash (issued set)
	status   map[string]string   // base64 hash -> key status
	creds    map[string]string   // base64 hash -> engine credential (provisioned set)
	ch       chan struct{}       // closed on every change (watch wake)
	failFeed bool                // feed answers 500 while set
	srv      *httptest.Server
	feedReqs int
}

func newIdentityFake(t *testing.T) *identityFake {
	t.Helper()
	f := &identityFake{
		keys:   map[string][32]byte{},
		status: map[string]string{},
		creds:  map[string]string{},
		ch:     make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/gateway/feed", f.handleFeed)
	mux.HandleFunc("/v1/gateway/keys/validate", f.handleValidate)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *identityFake) url() string { return f.srv.URL }

// issue records an active key; it returns the raw key.
func (f *identityFake) issue(raw string) string {
	return f.issueWithCredential(raw, "")
}

// issueProvisioned records an active key whose feed row carries the
// given engine credential (identity slice C contract).
func (f *identityFake) issueProvisioned(raw, credential string) string {
	return f.issueWithCredential(raw, credential)
}

func (f *identityFake) issueWithCredential(raw, credential string) string {
	hash := sha256.Sum256([]byte(raw))
	f.mu.Lock()
	f.keys[raw] = hash
	b64 := base64.StdEncoding.EncodeToString(hash[:])
	f.status[b64] = "active"
	if credential != "" {
		f.creds[b64] = credential
	}
	f.rev++
	close(f.ch)
	f.ch = make(chan struct{})
	f.mu.Unlock()
	return raw
}

// revoke flips a key to revoked without touching the issued set. The
// credential dies with the row — identity deletes the map entry on
// revocation, so the feed stops serving engine_credential immediately.
func (f *identityFake) revoke(raw string) {
	hash := sha256.Sum256([]byte(raw))
	f.mu.Lock()
	b64 := base64.StdEncoding.EncodeToString(hash[:])
	f.status[b64] = "revoked"
	delete(f.creds, b64)
	f.rev++
	close(f.ch)
	f.ch = make(chan struct{})
	f.mu.Unlock()
}

func (f *identityFake) failNextFeeds() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failFeed = true
}

func (f *identityFake) handleFeed(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.feedReqs++
	if f.failFeed {
		f.failFeed = false
		f.mu.Unlock()
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	since, _ := parseUintQuery(r.URL.Query().Get("since_rev"))
	rev := f.rev
	ch := f.ch
	if r.URL.Query().Get("watch") == "1" && rev <= since {
		f.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(40 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		f.mu.Lock()
		rev = f.rev
	}
	keys := make([]map[string]any, 0, len(f.status))
	for h, principal := range f.principalsLocked() {
		row := map[string]any{"hash": h, "principal_id": principal, "status": f.status[h]}
		if cred, ok := f.creds[h]; ok {
			row["engine_credential"] = cred
		}
		keys = append(keys, row)
	}
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"rev": rev, "keys": keys, "principals": []any{}})
}

// principalsLocked derives a principal per hash deterministically for
// the feed rows.
func (f *identityFake) principalsLocked() map[string]string {
	out := map[string]string{}
	for h := range f.status {
		out[h] = "p-" + h[:8]
	}
	return out
}

func (f *identityFake) handleValidate(w http.ResponseWriter, r *http.Request) {
	var req validateRequestDTO
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	hash, ok := f.keys[req.Key]
	status := f.status[base64.StdEncoding.EncodeToString(hash[:])]
	f.mu.Unlock()
	if !ok || status != "active" {
		http.Error(w, `{"error":{"code":"not_found"}}`, http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"principal_id": "p-" + base64.StdEncoding.EncodeToString(hash[:])[:8], "status": "active"})
}

func parseUintQuery(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	var n uint64
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errBadQuery
		}
		n = n*10 + uint64(c-'0')
	}
	return n, nil
}

var errBadQuery = errorString("bad query")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestClientSnapshotMapsWireShape(t *testing.T) {
	fake := newIdentityFake(t)
	fake.issue("lk-one")
	fake.issue("lk-two")
	fake.revoke("lk-two")

	client := NewIdentityClient(fake.url(), "token", 30*time.Second)
	resp, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if resp.Rev != 3 {
		t.Fatalf("want rev 3, got %d", resp.Rev)
	}
	if len(resp.Keys) != 2 {
		t.Fatalf("want both keys, got %d", len(resp.Keys))
	}
	statuses := map[string]int{}
	for _, k := range resp.Keys {
		statuses[k.Status]++
		if k.PrincipalID == "" {
			t.Fatal("principal must map through")
		}
	}
	if statuses["active"] != 1 || statuses["revoked"] != 1 {
		t.Fatalf("status mapping mismatch: %v", statuses)
	}
}

func TestClientWatchSendsParams(t *testing.T) {
	fake := newIdentityFake(t)
	fake.issue("lk-watch")

	client := NewIdentityClient(fake.url(), "token", 30*time.Second)
	start := time.Now()
	resp, err := client.Watch(context.Background(), 99) // far future: holds to timeout
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if resp.Rev != 1 {
		t.Fatalf("want rev 1, got %d", resp.Rev)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("watch client timeout must cover the watch window, took %v", elapsed)
	}
}

func TestClientMapsFailures(t *testing.T) {
	fake := newIdentityFake(t)
	client := NewIdentityClient(fake.url(), "token", 30*time.Second)

	fake.failNextFeeds()
	if _, err := client.Snapshot(context.Background()); err == nil {
		t.Fatal("a 500 feed must fail")
	}
	if _, err := client.Snapshot(context.Background()); err != nil {
		t.Fatalf("the next feed must succeed, got %v", err)
	}

	principalID, status, err := client.Validate(context.Background(), "lk-never-issued")
	if err == nil {
		t.Fatal("an unknown key must fail")
	}
	if !errors.Is(err, ErrKeyUnknown) {
		t.Fatalf("unknown key must map to ErrKeyUnknown, got %v", err)
	}
	if principalID != "" || status != "" {
		t.Fatal("a failed validate must not carry identity data")
	}

	raw := fake.issue("lk-validate")
	principalID, status, err = client.Validate(context.Background(), raw)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if principalID == "" || status != "active" {
		t.Fatalf("validate response mismatch: %q %q", principalID, status)
	}

	fake.srv.Close()
	if _, err := client.Snapshot(context.Background()); err == nil {
		t.Fatal("a refused origin must fail")
	}
	if _, _, err := client.Validate(context.Background(), raw); err == nil {
		t.Fatal("a refused validate must fail")
	}
}

func TestClientSendsServiceToken(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"rev": 1, "keys": []any{}, "principals": []any{}})
	}))
	defer srv.Close()
	client := NewIdentityClient(srv.URL, "unit-test-token", 30*time.Second)
	if _, err := client.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if gotToken != "Bearer unit-test-token" {
		t.Fatalf("service token must ride the Authorization header, got %q", gotToken)
	}
}

func TestClientFeedSourceInterface(t *testing.T) {
	// Compile-time anchor that IdentityClient satisfies the syncer's
	// seam (the var in identityclient.go is the real one; this keeps
	// the test-side honest if the signature drifts).
	var _ app.FeedSource = NewIdentityClient("http://unused", "t", time.Second)
}

// TestClientMapsEngineCredential pins the slice-C wire contract:
// engine_credential rides the feed row only for provisioned keys and
// maps through to the control-plane FeedKey; unprovisioned rows carry
// the empty value (the field is absent on the wire — never null).
func TestClientMapsEngineCredential(t *testing.T) {
	fake := newIdentityFake(t)
	fake.issue("lk-plain")
	fake.issueProvisioned("lk-provisioned", "engine-cred-42")

	client := NewIdentityClient(fake.url(), "token", 30*time.Second)
	resp, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(resp.Keys) != 2 {
		t.Fatalf("want both keys, got %d", len(resp.Keys))
	}
	got := map[string]string{}
	for _, k := range resp.Keys {
		got[string(k.Hash[:])] = k.EngineCredential
	}
	plain := sha256.Sum256([]byte("lk-plain"))
	prov := sha256.Sum256([]byte("lk-provisioned"))
	if cred := got[string(plain[:])]; cred != "" {
		t.Fatalf("an unprovisioned key must map an empty credential, got %q", cred)
	}
	if cred := got[string(prov[:])]; cred != "engine-cred-42" { //nolint:gosec // test-only fixture value, not a credential.
		t.Fatalf("a provisioned key must map its credential, got %q", cred)
	}
}
