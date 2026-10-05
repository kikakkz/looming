// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	"github.com/kikakkz/looming/identity/internal/policy/domain"
)

var testNowPolicy = func() time.Time {
	return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
}

func TestPolicyGetAdmin(t *testing.T) {
	store := &fakeStore{policy: &domain.Policy{
		ID: domain.SingletonID, Mode: domain.ModeInvite, UpdatedBy: "a", UpdatedAt: testNowPolicy(),
	}}
	svc := NewService(store, testNowPolicy)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/policy", NewHandler(svc).GetAdmin)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/policy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"mode":"invite"`)) {
		t.Fatalf("want invite mode in body, got %s", rec.Body.String())
	}
}

func TestPolicyGetDefaultsAdminOnly(t *testing.T) {
	svc := NewService(&fakeStore{}, testNowPolicy)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/policy", NewHandler(svc).GetAdmin)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/policy", nil))
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"mode":"admin-only"`)) {
		t.Fatalf("unset policy must read admin-only, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestPolicySetAdmin(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, testNowPolicy)
	mux := http.NewServeMux()
	h := NewHandler(svc)
	mux.HandleFunc("PUT /v1/admin/policy", func(w http.ResponseWriter, r *http.Request) {
		info := authnport.TokenInfo{PrincipalID: "admin-7", Roles: []string{"admin"}}
		h.SetAdmin(w, r.WithContext(authnport.WithTokenInfo(r.Context(), info)))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/policy",
		bytes.NewBufferString(`{"mode":"self-register-with-approval"}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(store.set) != 1 {
		t.Fatalf("one write expected, got %d", len(store.set))
	}
	if store.set[0].UpdatedBy != "admin-7" {
		t.Fatalf("audit column must name the acting admin, got %q", store.set[0].UpdatedBy)
	}
}

func TestPolicySetRejectsBadMode(t *testing.T) {
	svc := NewService(&fakeStore{}, testNowPolicy)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/admin/policy", NewHandler(svc).SetAdmin)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/policy",
		bytes.NewBufferString(`{"mode":"yolo"}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestPolicySetMalformedBody(t *testing.T) {
	svc := NewService(&fakeStore{}, testNowPolicy)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/admin/policy", NewHandler(svc).SetAdmin)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/policy", bytes.NewBufferString(`{`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}
