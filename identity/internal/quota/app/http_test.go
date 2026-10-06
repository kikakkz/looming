// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
)

// asSession stands in for the cmd auth middleware: it plants the
// validated session claims the handlers scope by.
func asSession(info authnport.TokenInfo, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(authnport.WithTokenInfo(r.Context(), info)))
	})
}

func newQuotaTestMux(svc *Service, info authnport.TokenInfo) *http.ServeMux {
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/admin/principals/{id}/quota", asSession(info, http.HandlerFunc(h.SetAdmin)).ServeHTTP)
	mux.HandleFunc("GET /v1/admin/principals/{id}/quota", asSession(info, http.HandlerFunc(h.GetAdmin)).ServeHTTP)
	mux.HandleFunc("GET /v1/self/quota", asSession(info, http.HandlerFunc(h.GetSelf)).ServeHTTP)
	return mux
}

func quotaTestAdminMux() (*http.ServeMux, *fakeProvisioner) {
	repo := newFakeQuotaRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{
		"p-1": activePrincipalMap("p-1"),
	}}
	maps := newFakeMapRepo()
	prov := newFakeProvisioner()
	svc := NewService(repo, principals, maps, prov, func() time.Time { return quotaTestNow })
	return newQuotaTestMux(svc, authnport.TokenInfo{PrincipalID: "admin-1", Roles: []string{"admin"}}), prov
}

func doQuotaReq(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func quotaErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("error response must be JSON, got %q", rec.Body.String())
	}
	errObj, _ := out["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	return code
}

func TestHTTPSetAdminQuota(t *testing.T) {
	mux, _ := quotaTestAdminMux()
	rec := doQuotaReq(t, mux, http.MethodPut, "/v1/admin/principals/p-1/quota",
		`{"amount":500,"unit":"usd","window_days":7}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response must be JSON: %v", err)
	}
	if out["principal_id"] != "p-1" || out["amount"] != float64(500) ||
		out["unit"] != "usd" || out["window_days"] != float64(7) || out["updated_by"] != "admin-1" {
		t.Fatalf("quota view mismatch: %v", out)
	}
	if _, ok := out["budget_update_failures"].([]any); !ok {
		t.Fatalf("budget_update_failures must always be present: %v", out)
	}

	rec = doQuotaReq(t, mux, http.MethodGet, "/v1/admin/principals/p-1/quota", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get after set: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestHTTPGetAdminQuotaNoQuota(t *testing.T) {
	mux, _ := quotaTestAdminMux()
	rec := doQuotaReq(t, mux, http.MethodGet, "/v1/admin/principals/p-ghost/quota", "")
	// Unknown principal also lands on no_quota: the repository is the
	// only authority and it holds no row for ghosts.
	if rec.Code != http.StatusNotFound || quotaErrCode(t, rec) != "no_quota" {
		t.Fatalf("want 404 no_quota, got %d %q", rec.Code, quotaErrCode(t, rec))
	}
}

func TestHTTPSetAdminQuotaValidation(t *testing.T) {
	mux, _ := quotaTestAdminMux()
	cases := []struct {
		name string
		body string
	}{
		{"bad unit", `{"amount":5,"unit":"requests","window_days":7}`},
		{"bad window", `{"amount":5,"unit":"usd","window_days":13}`},
		{"negative amount", `{"amount":-5,"unit":"usd","window_days":7}`},
		{"missing amount", `{"unit":"usd","window_days":7}`},
		{"missing unit", `{"amount":5,"window_days":7}`},
		{"missing window", `{"amount":5,"unit":"usd"}`},
		{"malformed", `not-json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doQuotaReq(t, mux, http.MethodPut, "/v1/admin/principals/p-1/quota", tc.body)
			if rec.Code != http.StatusBadRequest || quotaErrCode(t, rec) != "invalid_request" {
				t.Fatalf("want 400 invalid_request, got %d %q", rec.Code, quotaErrCode(t, rec))
			}
		})
	}
}

func TestHTTPSetAdminQuotaUnknownPrincipal(t *testing.T) {
	mux, _ := quotaTestAdminMux()
	rec := doQuotaReq(t, mux, http.MethodPut, "/v1/admin/principals/p-ghost/quota",
		`{"amount":5,"unit":"usd","window_days":7}`)
	if rec.Code != http.StatusNotFound || quotaErrCode(t, rec) != "not_found" {
		t.Fatalf("want 404 not_found, got %d %q", rec.Code, quotaErrCode(t, rec))
	}
}

func TestHTTPSetAdminQuotaCollectsFailuresWith200(t *testing.T) {
	repo := newFakeQuotaRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipalMap("p-1")}}
	maps := newFakeMapRepo()
	m1, _ := provisiondomain.NewIdentityMap("k-1", "litellm", "ref-1", []byte("enc"), quotaTestNow)
	m2, _ := provisiondomain.NewIdentityMap("k-2", "litellm", "ref-2", []byte("enc"), quotaTestNow)
	maps.byID["k-1|litellm"] = m1
	maps.byID["k-2|litellm"] = m2
	maps.owner["k-1|litellm"] = "p-1"
	maps.owner["k-2|litellm"] = "p-1"
	prov := newFakeProvisioner()
	prov.budgetErrs["ref-1"] = errors.New("engine 500")
	svc := NewService(repo, principals, maps, prov, func() time.Time { return quotaTestNow })
	mux := newQuotaTestMux(svc, authnport.TokenInfo{PrincipalID: "admin-1", Roles: []string{"admin"}})

	rec := doQuotaReq(t, mux, http.MethodPut, "/v1/admin/principals/p-1/quota",
		`{"amount":700,"unit":"usd","window_days":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("projection failures must not fail the set, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response must be JSON: %v", err)
	}
	failures, ok := out["budget_update_failures"].([]any)
	if !ok || len(failures) != 1 {
		t.Fatalf("want one listed failure, got %v", out["budget_update_failures"])
	}
	failure, _ := failures[0].(map[string]any)
	if failure["credential_ref"] != "ref-1" || failure["key_id"] != "k-1" || failure["message"] == "" {
		t.Fatalf("failure entry mismatch: %v", failure)
	}
}

func TestHTTPSelfQuotaView(t *testing.T) {
	repo := newFakeQuotaRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{
		"p-1": activePrincipalMap("p-1"),
	}}
	svc := NewService(repo, principals, newFakeMapRepo(), nil, func() time.Time { return quotaTestNow })
	mux := newQuotaTestMux(svc, authnport.TokenInfo{PrincipalID: "p-1", Roles: []string{"member"}})

	rec := doQuotaReq(t, mux, http.MethodGet, "/v1/self/quota", "")
	if rec.Code != http.StatusNotFound || quotaErrCode(t, rec) != "no_quota" {
		t.Fatalf("unset self quota: want 404 no_quota, got %d %q", rec.Code, quotaErrCode(t, rec))
	}

	rec = doQuotaReq(t, mux, http.MethodPut, "/v1/admin/principals/p-1/quota",
		`{"amount":42,"unit":"tokens","window_days":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set: %d (%s)", rec.Code, rec.Body.String())
	}
	rec = doQuotaReq(t, mux, http.MethodGet, "/v1/self/quota", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("self view: %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response must be JSON: %v", err)
	}
	if out["amount"] != float64(42) || out["unit"] != "tokens" || out["principal_id"] != "p-1" {
		t.Fatalf("self quota mismatch: %v", out)
	}
}
