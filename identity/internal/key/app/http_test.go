// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
)

// newKeyTestMux mirrors the key route table cmd/identityd wires; the
// asSession wrapper stands in for the cmd auth middleware.
func asSession(info authnport.TokenInfo, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(authnport.WithTokenInfo(r.Context(), info)))
	})
}

func newKeyTestMux(svc *Service, info authnport.TokenInfo) *http.ServeMux {
	h := NewHandler(svc)
	mux := http.NewServeMux()
	session := asSession(info, http.HandlerFunc(h.IssueSelf)).ServeHTTP
	mux.HandleFunc("POST /v1/self/keys", session)
	mux.HandleFunc("GET /v1/self/keys", asSession(info, http.HandlerFunc(h.ListSelf)).ServeHTTP)
	mux.HandleFunc("GET /v1/self/keys/{id}", asSession(info, http.HandlerFunc(h.GetSelf)).ServeHTTP)
	mux.HandleFunc("POST /v1/self/keys/{id}/reveal", asSession(info, http.HandlerFunc(h.RevealSelf)).ServeHTTP)
	mux.HandleFunc("DELETE /v1/self/keys/{id}", asSession(info, http.HandlerFunc(h.RevokeSelf)).ServeHTTP)
	mux.HandleFunc("GET /v1/admin/principals/{id}/keys", asSession(info, http.HandlerFunc(h.ListForPrincipalAdmin)).ServeHTTP)
	mux.HandleFunc("DELETE /v1/admin/keys/{id}", asSession(info, http.HandlerFunc(h.RevokeAdmin)).ServeHTTP)
	return mux
}

func keyTestService() (*Service, *fakeKeyRepo) {
	repo := newFakeKeyRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{
		"p-1": activePrincipal("p-1"),
		"p-2": activePrincipal("p-2"),
	}}
	return newTestService(repo, principals, &fakeSealer{}), repo
}

func doKeyReq(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func keyErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("error response must be JSON, got %q", rec.Body.String())
	}
	errObj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("error response must carry the error object, got %v", out)
	}
	code, _ := errObj["code"].(string)
	return code
}

func TestHTTPKeyIssueSelf(t *testing.T) {
	svc, _ := keyTestService()
	mux := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})
	rec := doKeyReq(t, mux, http.MethodPost, "/v1/self/keys", `{"name":"ci bot"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response must be JSON: %v", err)
	}
	if out["id"] == "" || out["name"] != "ci bot" {
		t.Fatalf("issue response mismatch: %v", out)
	}
	raw, ok := out["key"].(string)
	if !ok || len(raw) < 10 || raw[:3] != "lk-" {
		t.Fatalf("issue response must carry the raw key once, got %v", out["key"])
	}
}

func TestHTTPKeyIssueValidationAndGates(t *testing.T) {
	svc, _ := keyTestService()
	mux := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})

	// An empty (omitted) name is the optional-label default and passes.
	rec := doKeyReq(t, mux, http.MethodPost, "/v1/self/keys", `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("empty name must pass, got %d (%s)", rec.Code, rec.Body.String())
	}

	long := bytes.Repeat([]byte("n"), 129)
	rec = doKeyReq(t, mux, http.MethodPost, "/v1/self/keys", `{"name":"`+string(long)+`"}`)
	if rec.Code != http.StatusBadRequest || keyErrCode(t, rec) != "invalid_request" {
		t.Fatalf("over-long name: want 400 invalid_request, got %d %q", rec.Code, keyErrCode(t, rec))
	}
}

func TestHTTPKeyListMasksSecrets(t *testing.T) {
	svc, _ := keyTestService()
	mux := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})
	rec := doKeyReq(t, mux, http.MethodPost, "/v1/self/keys", `{"name":"one"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("issue: %d", rec.Code)
	}
	rec = doKeyReq(t, mux, http.MethodGet, "/v1/self/keys", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("list must be JSON: %v", err)
	}
	items, ok := out["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("want one item, got %v", out)
	}
	view, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item shape mismatch: %v", items[0])
	}
	for _, forbidden := range []string{"key", "key_hash", "hash", "sealed", "key_enc"} {
		if _, leaked := view[forbidden]; leaked {
			t.Fatalf("masked list leaked %q: %v", forbidden, view)
		}
	}
	for _, want := range []string{"id", "name", "prefix", "last4", "status", "created_at"} {
		if _, present := view[want]; !present {
			t.Fatalf("masked list missing %q: %v", want, view)
		}
	}
	if view["status"] != "active" {
		t.Fatalf("status mismatch: %v", view)
	}
}

func TestHTTPKeyGetAndRevealFlow(t *testing.T) {
	svc, _ := keyTestService()
	mux := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})
	rec := doKeyReq(t, mux, http.MethodPost, "/v1/self/keys", `{"name":"rev"}`)
	var issued map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &issued)
	id, _ := issued["id"].(string)

	rec = doKeyReq(t, mux, http.MethodGet, "/v1/self/keys/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"key"`)) {
		t.Fatal("masked get must not carry the secret")
	}

	rec = doKeyReq(t, mux, http.MethodPost, "/v1/self/keys/"+id+"/reveal", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal: %d (%s)", rec.Code, rec.Body.String())
	}
	var revealed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &revealed)
	if revealed["key"] != issued["key"] {
		t.Fatalf("reveal must return the issued secret: %v vs %v", revealed["key"], issued["key"])
	}
	// Repeatable reveal.
	rec = doKeyReq(t, mux, http.MethodPost, "/v1/self/keys/"+id+"/reveal", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("second reveal: %d", rec.Code)
	}
}

func TestHTTPKeyCrossOwnerProbesAreNotFound(t *testing.T) {
	svc, _ := keyTestService()
	owner := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})
	rec := doKeyReq(t, owner, http.MethodPost, "/v1/self/keys", `{"name":"mine"}`)
	var issued map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &issued)
	id, _ := issued["id"].(string)

	stranger := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-2", Roles: []string{"member"}})
	rec = doKeyReq(t, stranger, http.MethodGet, "/v1/self/keys/"+id, "")
	if rec.Code != http.StatusNotFound || keyErrCode(t, rec) != "not_found" {
		t.Fatalf("cross-owner get: want 404 not_found, got %d %q", rec.Code, keyErrCode(t, rec))
	}
	rec = doKeyReq(t, stranger, http.MethodPost, "/v1/self/keys/"+id+"/reveal", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner reveal: want 404, got %d", rec.Code)
	}
	rec = doKeyReq(t, stranger, http.MethodDelete, "/v1/self/keys/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner revoke: want 404, got %d", rec.Code)
	}
}

func TestHTTPKeyRevokeSelf(t *testing.T) {
	svc, _ := keyTestService()
	mux := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})
	rec := doKeyReq(t, mux, http.MethodPost, "/v1/self/keys", `{"name":"die"}`)
	var issued map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &issued)
	id, _ := issued["id"].(string)

	rec = doKeyReq(t, mux, http.MethodDelete, "/v1/self/keys/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d (%s)", rec.Code, rec.Body.String())
	}
	rec = doKeyReq(t, mux, http.MethodDelete, "/v1/self/keys/"+id, "")
	if rec.Code != http.StatusConflict || keyErrCode(t, rec) != "already_revoked" {
		t.Fatalf("double revoke: want 409 already_revoked, got %d %q", rec.Code, keyErrCode(t, rec))
	}
}

func TestHTTPKeyAdminRoutes(t *testing.T) {
	svc, _ := keyTestService()
	user := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})
	rec := doKeyReq(t, user, http.MethodPost, "/v1/self/keys", `{"name":"admin-me"}`)
	var issued map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &issued)
	id, _ := issued["id"].(string)

	admin := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "admin-1", Roles: []string{"admin"}})
	rec = doKeyReq(t, admin, http.MethodGet, "/v1/admin/principals/p-1/keys", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list: %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if items, _ := out["items"].([]any); len(items) != 1 {
		t.Fatalf("admin list wants one masked key, got %v", out)
	}

	// Admin reveal through the self reveal route (owner or admin).
	rec = doKeyReq(t, admin, http.MethodPost, "/v1/self/keys/"+id+"/reveal", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin reveal via self route: %d (%s)", rec.Code, rec.Body.String())
	}
	var revealed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &revealed)
	if revealed["key"] != issued["key"] {
		t.Fatal("admin reveal must return the issued secret")
	}

	rec = doKeyReq(t, admin, http.MethodDelete, "/v1/admin/keys/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin revoke: want 204, got %d", rec.Code)
	}
	rec = doKeyReq(t, admin, http.MethodDelete, "/v1/admin/keys/"+id, "")
	if rec.Code != http.StatusConflict || keyErrCode(t, rec) != "already_revoked" {
		t.Fatalf("admin double revoke: want 409 already_revoked, got %d", rec.Code)
	}
}

func TestHTTPKeyErrorContract(t *testing.T) {
	repo := newFakeKeyRepo()
	repo.count = 50
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}
	svc := newTestService(repo, principals, &fakeSealer{})
	mux := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})

	rec := doKeyReq(t, mux, http.MethodPost, "/v1/self/keys", `{"name":""}`)
	if rec.Code != http.StatusTooManyRequests || keyErrCode(t, rec) != "issue_limit" {
		t.Fatalf("rate limit: want 429 issue_limit, got %d %q", rec.Code, keyErrCode(t, rec))
	}
	// A missing session maps to the empty actor and 404s downstream.
	muxNoSession := http.NewServeMux()
	h := NewHandler(svc)
	muxNoSession.HandleFunc("POST /v1/self/keys", h.IssueSelf)
	rec = doKeyReq(t, muxNoSession, http.MethodPost, "/v1/self/keys", `{"name":""}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no-session issue: want 404, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if msg, _ := out["error"].(map[string]any)["message"].(string); msg == "" || msg == ErrPrincipalNotFound.Error() {
		t.Fatalf("unsafe errors must return the code only, got %v", out)
	}
}

func TestHTTPKeyMaskedViewShape(t *testing.T) {
	svc, _ := keyTestService()
	mux := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})
	doKeyReq(t, mux, http.MethodPost, "/v1/self/keys", `{"name":"shape"}`)
	rec := doKeyReq(t, mux, http.MethodGet, "/v1/self/keys", "")
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	items := out["items"].([]any)
	view := items[0].(map[string]any)
	wantKeys := []string{"created_at", "id", "last4", "name", "prefix", "status"}
	gotKeys := make([]string, 0, len(view))
	for k := range view {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	if diff := cmp.Diff(wantKeys, gotKeys); diff != "" {
		t.Fatalf("masked view keys mismatch (-want +got):\n%s", diff)
	}
}

func TestHTTPKeyIssueProvisionFailedShape(t *testing.T) {
	repo := newFakeKeyRepo()
	prov := newFakeProvisioner()
	prov.createErr = errors.New("engine 500")
	svc := newTestService(repo, &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}, &fakeSealer{})
	svc.SetEngineProvisioner(prov, newFakeMapRepo(), &fakeQuotaRepo{}, "litellm")
	mux := newKeyTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})

	rec := doKeyReq(t, mux, http.MethodPost, "/v1/self/keys", `{"name":"x"}`)
	if rec.Code != http.StatusBadGateway || keyErrCode(t, rec) != "provision_failed" {
		t.Fatalf("want 502 provision_failed, got %d %q", rec.Code, keyErrCode(t, rec))
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if msg, _ := out["error"].(map[string]any)["message"].(string); msg != "provision_failed" {
		t.Fatalf("unsafe errors must return the code only, got %v", out)
	}
	if len(repo.byID) != 0 {
		t.Fatal("a failed provision must persist nothing")
	}
}
