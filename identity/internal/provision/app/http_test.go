// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
)

func newInspectMux(svc *Service) *http.ServeMux {
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/identitymap", h.InspectAdmin)
	return mux
}

func doInspectReq(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHTTPInspectWireShape(t *testing.T) {
	m, _ := provisiondomain.NewIdentityMap("k-1", "engine:p-1", "ref-1", []byte("sealed-blob-bytes"), testNow)
	repo := newFakeMapRepo()
	repo.byID["k-1|engine:p-1"] = m
	mux := newInspectMux(NewService(repo))

	rec := doInspectReq(t, mux, "/v1/admin/identitymap?principal=p-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response must be JSON: %v", err)
	}
	if out["total"] != float64(1) {
		t.Fatalf("total mismatch: %v", out["total"])
	}
	items, ok := out["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items shape mismatch: %v", out["items"])
	}
	entry, _ := items[0].(map[string]any)
	for _, field := range []string{"key_id", "engine", "credential_ref", "status", "created_at", "updated_at"} {
		if _, ok := entry[field]; !ok {
			t.Fatalf("entry must carry %s: %v", field, entry)
		}
	}
	if entry["credential_ref"] != "ref-1" || entry["key_id"] != "k-1" || entry["status"] != "active" {
		t.Fatalf("entry mismatch: %v", entry)
	}
}

func TestHTTPInspectNeverLeaksCredentialMaterial(t *testing.T) {
	m, _ := provisiondomain.NewIdentityMap("k-1", "engine:p-1", "ref-1", []byte("sealed-blob-bytes"), testNow)
	repo := newFakeMapRepo()
	repo.byID["k-1|engine:p-1"] = m
	mux := newInspectMux(NewService(repo))

	rec := doInspectReq(t, mux, "/v1/admin/identitymap?principal=p-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	for _, forbidden := range []string{"sealed-blob-bytes", "credential_enc", "engine_credential"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("inspect response must never carry %q: %s", forbidden, body)
		}
	}
}

func TestHTTPInspectParameterValidation(t *testing.T) {
	mux := newInspectMux(NewService(newFakeMapRepo()))
	cases := []struct {
		name string
		path string
	}{
		{"missing principal", "/v1/admin/identitymap"},
		{"bad limit", "/v1/admin/identitymap?principal=p-1&limit=-1"},
		{"huge limit", "/v1/admin/identitymap?principal=p-1&limit=501"},
		{"non-numeric limit", "/v1/admin/identitymap?principal=p-1&limit=abc"},
		{"bad offset", "/v1/admin/identitymap?principal=p-1&offset=-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doInspectReq(t, mux, tc.path)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHTTPInspectEmptyPrincipal(t *testing.T) {
	mux := newInspectMux(NewService(newFakeMapRepo()))
	rec := doInspectReq(t, mux, "/v1/admin/identitymap?principal=nobody")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with an empty page, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response must be JSON: %v", err)
	}
	if out["total"] != float64(0) {
		t.Fatalf("empty principal must page empty, got %v", out["total"])
	}
}
