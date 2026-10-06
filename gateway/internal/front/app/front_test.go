// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kikakkz/looming/gateway/internal/engineplane"
	frontdomain "github.com/kikakkz/looming/gateway/internal/front/domain"
	"github.com/kikakkz/looming/gateway/internal/front/port"
	"go.uber.org/goleak"
)

type stubAuthn struct {
	subject    string
	credential string
}

func (s stubAuthn) Authenticate(_ context.Context, key string) (port.Identity, error) {
	if key == "good-key" {
		return port.Identity{Subject: s.subject, EngineCredential: s.credential}, nil
	}
	return port.Identity{}, errors.New("bad key")
}

type frontTestLink struct{ v frontdomain.Verdict }

func (s frontTestLink) Evaluate(_ context.Context, _ frontdomain.LinkContext, _ *http.Request) (frontdomain.Verdict, error) {
	return s.v, nil
}

type stubAllowlist struct{ err error }

func (s stubAllowlist) Models(_ context.Context, _ string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []string{"gpt-5"}, nil
}

// credentialRecordingEngine captures what the pipeline hands the engine
// slot so the wiring contract is asserted directly.
type credentialRecordingEngine struct {
	mu         sync.Mutex
	credential string
	sawLoomKey bool
	body       string
}

func (s *credentialRecordingEngine) Forward(_ context.Context, w http.ResponseWriter, r *http.Request, credential string) error {
	s.mu.Lock()
	s.credential = credential
	auth := r.Header.Get("Authorization")
	s.sawLoomKey = auth == "Bearer good-key" || strings.Contains(auth, "good-key")
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, err := io.WriteString(w, s.body)
	return err
}

type headerExtractor struct{}

func (headerExtractor) Extract(r *http.Request) (string, error) {
	return r.Header.Get("X-Test-Model"), nil
}

func newTestFront(engineBody string) *Front {
	return NewFront(
		stubAuthn{subject: "ker"},
		stubAllowlist{},
		frontdomain.NewChain(frontTestLink{v: frontdomain.VerdictPass}),
		&credentialRecordingEngine{body: engineBody},
		headerExtractor{},
		nil,
	)
}

func TestFrontPipelinePass(t *testing.T) {
	defer goleak.VerifyNone(t)
	front := newTestFront("ok")
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer good-key")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("want 200 ok, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestFrontRejectsBadKey(t *testing.T) {
	defer goleak.VerifyNone(t)
	front := newTestFront("ok")
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer nope")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestFrontRejectsUnlistedModel(t *testing.T) {
	defer goleak.VerifyNone(t)
	front := newTestFront("ok")
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer good-key")
	req.Header.Set("X-Test-Model", "claude-3")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("authenticated model denial is 403, got %d", rec.Code)
	}
}

func TestFrontAllowlistLookupFailureIsServiceUnavailable(t *testing.T) {
	defer goleak.VerifyNone(t)
	front := NewFront(stubAuthn{subject: "ker"}, stubAllowlist{err: errors.New("authority down")},
		frontdomain.NewChain(frontTestLink{v: frontdomain.VerdictPass}), &credentialRecordingEngine{body: "ok"},
		headerExtractor{}, nil)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer good-key")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("lookup failure is a 503 fault, got %d", rec.Code)
	}
}

func TestFrontEngineFailureIsBadGateway(t *testing.T) {
	defer goleak.VerifyNone(t)
	broken := stubEngineFunc(func(http.ResponseWriter, *http.Request) error {
		return engineplane.ErrNotImplemented
	})
	front := NewFront(stubAuthn{subject: "ker"}, stubAllowlist{},
		frontdomain.NewChain(frontTestLink{v: frontdomain.VerdictPass}), broken,
		headerExtractor{}, nil)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer good-key")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
}

type stubEngineFunc func(http.ResponseWriter, *http.Request) error

func (s stubEngineFunc) Forward(_ context.Context, w http.ResponseWriter, r *http.Request, _ string) error {
	return s(w, r)
}

// --- slice C: engine credential wiring + the non-leak invariant ---

// TestFrontPassesEngineCredentialToEngine pins the pipeline contract:
// the Authenticator's EngineCredential travels into the engine call as
// the explicit credential parameter, and the raw LoomingKey never
// crosses into the engine path (no header smuggling, no param reuse).
func TestFrontPassesEngineCredentialToEngine(t *testing.T) {
	defer goleak.VerifyNone(t)
	engine := &credentialRecordingEngine{body: "ok"}
	front := NewFront(
		stubAuthn{subject: "ker", credential: "engine-cred-xyz"},
		stubAllowlist{},
		frontdomain.NewChain(frontTestLink{v: frontdomain.VerdictPass}),
		engine,
		headerExtractor{},
		nil,
	)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5"}`))
	req.Header.Set("Authorization", "Bearer good-key")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.credential != "engine-cred-xyz" {
		t.Fatalf("the engine must receive the provisioned credential, got %q", engine.credential)
	}
	if engine.credential == "good-key" || engine.sawLoomKey {
		t.Fatal("the LoomingKey must never enter the engine call path")
	}
}

// TestFrontUnprovisionedKeyForwardsEmptyCredential pins the fallback
// contract: an identity without a credential (never provisioned, or a
// validate-fallback before the feed caught up) forwards "" and the
// engine applies its own default (static upstream auth or strip).
func TestFrontUnprovisionedKeyForwardsEmptyCredential(t *testing.T) {
	defer goleak.VerifyNone(t)
	engine := &credentialRecordingEngine{body: "ok"}
	front := NewFront(
		stubAuthn{subject: "ker"},
		stubAllowlist{},
		frontdomain.NewChain(frontTestLink{v: frontdomain.VerdictPass}),
		engine,
		headerExtractor{},
		nil,
	)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer good-key")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.credential != "" {
		t.Fatalf("an unprovisioned key must forward an empty credential, got %q", engine.credential)
	}
}

type recordingQueue struct{ bodies []frontdomain.InteractionBody }

func (q *recordingQueue) Enqueue(_ context.Context, b frontdomain.InteractionBody) {
	q.bodies = append(q.bodies, b)
}

type recordingMeter struct{ records []frontdomain.MeterRecord }

func (m *recordingMeter) Record(_ context.Context, r frontdomain.MeterRecord) {
	m.records = append(m.records, r)
}

// TestFrontRecordingNeverCarriesCredentials pins the transcript
// invariant for the credential path (slice C): the recorder captures
// bodies only — InteractionBody carries no header fields at all — and
// the captured bytes must contain neither the LoomingKey nor the engine
// credential. Both secrets are distinct canaries so a swap or bleed is
// caught either way.
func TestFrontRecordingNeverCarriesCredentials(t *testing.T) {
	defer goleak.VerifyNone(t)
	const loomKey = "lk-canary-never-leak"
	//nolint:gosec // test-only canary string, not a credential.
	const engineCred = "eng-cred-canary-never-leak"
	authn := stubAuthnFunc(func(_ context.Context, key string) (port.Identity, error) {
		if key == loomKey {
			return port.Identity{Subject: "ker", EngineCredential: engineCred}, nil
		}
		return port.Identity{}, errors.New("bad key")
	})
	queue := &recordingQueue{}
	meter := &recordingMeter{}
	front := NewFront(
		authn,
		stubAllowlist{},
		frontdomain.NewChain(frontTestLink{v: frontdomain.VerdictPass}),
		&credentialRecordingEngine{body: "model says hi"},
		headerExtractor{},
		nil,
		WithRecording(queue, meter, 1<<20),
	)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","prompt":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+loomKey)
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
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

// TestFrontLogsNeverCarryCredentials pins the log-side hygiene: denials
// log the key hash, never the raw key or the credential.
func TestFrontLogsNeverCarryCredentials(t *testing.T) {
	defer goleak.VerifyNone(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	front := NewFront(
		stubAuthn{subject: "ker", credential: "eng-cred-log-canary"},
		stubAllowlist{},
		frontdomain.NewChain(frontTestLink{v: frontdomain.VerdictPass}),
		&credentialRecordingEngine{body: "ok"},
		headerExtractor{},
		log,
	)

	// A denial (401) and a pass (200) both exercise the log paths.
	for _, key := range []string{"good-key", "bad-key"} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Test-Model", "gpt-5")
		rec := httptest.NewRecorder()
		front.ServeHTTP(rec, req)
	}

	out := buf.String()
	// The bad-key request is the denied one: its log line carries the
	// key hash, never the raw key.
	if !strings.Contains(out, "key_sha="+hashKey("bad-key")) {
		t.Fatalf("denial/audit lines must carry the key hash for correlation, got: %s", out)
	}
	for _, secret := range []string{"good-key", "bad-key", "eng-cred-log-canary"} {
		if strings.Contains(out, secret) {
			t.Fatalf("logs must never carry %q: %s", secret, out)
		}
	}
}

type stubAuthnFunc func(context.Context, string) (port.Identity, error)

func (s stubAuthnFunc) Authenticate(ctx context.Context, key string) (port.Identity, error) {
	return s(ctx, key)
}
