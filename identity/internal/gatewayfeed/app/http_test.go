// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	feeddomain "github.com/kikakkz/looming/identity/internal/gatewayfeed/domain"
)

const testToken = "feed-token"

var errFakeStore = errorString("fake store down")

type errorString string

func (e errorString) Error() string { return string(e) }

func newFeedMux(svc *Service) *http.ServeMux {
	h := NewHandler(svc)
	mux := http.NewServeMux()
	guarded := func(next http.HandlerFunc) http.Handler {
		return RequireServiceToken(testToken, http.HandlerFunc(next))
	}
	mux.Handle("GET /v1/gateway/feed", guarded(h.Feed))
	mux.Handle("POST /v1/gateway/keys/validate", guarded(h.Validate))
	return mux
}

func doFeedReq(t *testing.T, mux *http.ServeMux, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestServiceTokenMiddlewareShapes(t *testing.T) {
	svc := newTestService(&fakeStore{}, time.Second)
	mux := newFeedMux(svc)

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong token", "wrong", http.StatusUnauthorized},
		{"good token", testToken, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doFeedReq(t, mux, http.MethodGet, "/v1/gateway/feed", "", tc.token)
			if rec.Code != tc.want {
				t.Fatalf("want %d, got %d (%s)", tc.want, rec.Code, rec.Body.String())
			}
			if tc.want == http.StatusUnauthorized {
				var out map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &out)
				errObj, ok := out["error"].(map[string]any)
				if !ok || errObj["code"] != "unauthenticated" {
					t.Fatalf("401 shape mismatch: %v", out)
				}
			}
		})
	}
}

func TestFeedSnapshotWireShape(t *testing.T) {
	store := &fakeStore{
		keys: []feeddomain.Key{
			{Hash: []byte("0123456789abcdef"), PrincipalID: "p-1", Status: feeddomain.KeyActive},
			{Hash: []byte("fedcba9876543210"), PrincipalID: "p-1", Status: feeddomain.KeyRevoked},
		},
		principals: []feeddomain.Principal{
			{ID: "p-1", Status: feeddomain.PrincipalActive},
			{ID: "p-2", Status: feeddomain.PrincipalDisabled},
		},
	}
	svc := newTestService(store, time.Second)
	svc.hub.Bump()
	mux := newFeedMux(svc)

	rec := doFeedReq(t, mux, http.MethodGet, "/v1/gateway/feed", "", testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("feed must be JSON: %v", err)
	}
	if out["rev"] != float64(1) {
		t.Fatalf("rev mismatch: %v", out["rev"])
	}
	keys, ok := out["keys"].([]any)
	if !ok || len(keys) != 2 {
		t.Fatalf("keys shape mismatch: %v", out["keys"])
	}
	first := keys[0].(map[string]any)
	if first["principal_id"] != "p-1" || first["status"] != "active" {
		t.Fatalf("key row mismatch: %v", first)
	}
	if _, err := base64.StdEncoding.DecodeString(first["hash"].(string)); err != nil {
		t.Fatalf("hash must be base64: %v", err)
	}
	principals := out["principals"].([]any)
	if len(principals) != 2 {
		t.Fatalf("principals shape mismatch: %v", out["principals"])
	}
}

func TestFeedWatchParamValidation(t *testing.T) {
	svc := newTestService(&fakeStore{}, time.Second)
	mux := newFeedMux(svc)
	rec := doFeedReq(t, mux, http.MethodGet, "/v1/gateway/feed?watch=1&since_rev=-3", "", testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative since_rev: want 400, got %d", rec.Code)
	}
	rec = doFeedReq(t, mux, http.MethodGet, "/v1/gateway/feed?watch=1&since_rev=abc", "", testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric since_rev: want 400, got %d", rec.Code)
	}
}

func TestFeedWatchEndpointHoldsThenReturns(t *testing.T) {
	svc := newTestService(&fakeStore{}, 80*time.Millisecond)
	mux := newFeedMux(svc)

	start := time.Now()
	rec := doFeedReq(t, mux, http.MethodGet, "/v1/gateway/feed?watch=1&since_rev=0", "", testToken)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("watch feed: %d", rec.Code)
	}
	if elapsed < 80*time.Millisecond {
		t.Fatalf("watch must hold for the timeout when unbumped, released after %v", elapsed)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["rev"] != float64(0) {
		t.Fatalf("timeout returns the current snapshot: %v", out["rev"])
	}
}

func TestValidateEndpoint(t *testing.T) {
	rawKey := "lk-validateme"
	hash := sha256.Sum256([]byte(rawKey))
	store := &fakeStore{byHash: map[string]fakeLookup{
		string(hash[:]): {key: feeddomain.Key{Hash: hash[:], PrincipalID: "p-1", Status: feeddomain.KeyActive}, principalStatus: feeddomain.PrincipalActive},
	}}
	svc := newTestService(store, time.Second)
	mux := newFeedMux(svc)

	rec := doFeedReq(t, mux, http.MethodPost, "/v1/gateway/keys/validate", `{"key":"`+rawKey+`"}`, testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate: %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["principal_id"] != "p-1" || out["status"] != "active" {
		t.Fatalf("validate response mismatch: %v", out)
	}

	rec = doFeedReq(t, mux, http.MethodPost, "/v1/gateway/keys/validate", `{"key":"lk-unknown"}`, testToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown key: want 404, got %d", rec.Code)
	}
	var errOut map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &errOut)
	if errOut["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("404 shape mismatch: %v", errOut)
	}

	rec = doFeedReq(t, mux, http.MethodPost, "/v1/gateway/keys/validate", `{}`, testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key field: want 400, got %d", rec.Code)
	}
	rec = doFeedReq(t, mux, http.MethodPost, "/v1/gateway/keys/validate", `not-json`, testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: want 400, got %d", rec.Code)
	}
}

func TestValidateEndpointFailClosedFromService(t *testing.T) {
	// A revoked key resolves from the store but must still 404.
	rawKey := "lk-revokedkey"
	hash := sha256.Sum256([]byte(rawKey))
	store := &fakeStore{byHash: map[string]fakeLookup{
		string(hash[:]): {key: feeddomain.Key{Hash: hash[:], PrincipalID: "p-1", Status: feeddomain.KeyRevoked}, principalStatus: feeddomain.PrincipalActive},
	}}
	svc := newTestService(store, time.Second)
	mux := newFeedMux(svc)
	rec := doFeedReq(t, mux, http.MethodPost, "/v1/gateway/keys/validate", `{"key":"`+rawKey+`"}`, testToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoked key: want 404, got %d", rec.Code)
	}
}

func TestFeedStoreFailureIs503(t *testing.T) {
	svc := newTestService(&fakeStore{err: errFakeStore}, time.Second)
	mux := newFeedMux(svc)
	rec := doFeedReq(t, mux, http.MethodGet, "/v1/gateway/feed", "", testToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("store failure: want 503, got %d", rec.Code)
	}
	rec = doFeedReq(t, mux, http.MethodPost, "/v1/gateway/keys/validate", `{"key":"x"}`, testToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("validate store failure: want 503, got %d", rec.Code)
	}
}

func TestFeedSnapshotCarriesEngineCredentialOnlyWhenProvisioned(t *testing.T) {
	store := &fakeStore{
		keys: []feeddomain.Key{
			{Hash: []byte("0123456789abcdef"), PrincipalID: "p-1", Status: feeddomain.KeyActive, EngineCredential: "sk-engine-value"},
			{Hash: []byte("fedcba9876543210"), PrincipalID: "p-1", Status: feeddomain.KeyActive},
		},
	}
	svc := newTestService(store, time.Second)
	mux := newFeedMux(svc)

	rec := doFeedReq(t, mux, http.MethodGet, "/v1/gateway/feed", "", testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("feed must be JSON: %v", err)
	}
	keys := out["keys"].([]any)
	provisioned, _ := keys[0].(map[string]any)
	if provisioned["engine_credential"] != "sk-engine-value" {
		t.Fatalf("provisioned key must carry the credential: %v", provisioned)
	}
	plain, _ := keys[1].(map[string]any)
	if _, ok := plain["engine_credential"]; ok {
		t.Fatalf("unprovisioned key must omit the field entirely: %v", plain)
	}
}
