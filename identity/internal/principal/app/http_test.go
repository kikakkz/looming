// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	policydomain "github.com/kikakkz/looming/identity/internal/policy/domain"
)

// newTestMux mirrors the route table cmd/identityd wires (app handlers
// mounted per route; session injection stands in for the cmd auth
// middleware, which is tested at the cmd layer).
func newTestMux(pol *fakePolicy) (*http.ServeMux, *fakeInvites) {
	repo := newFakeRepo()
	invites := newFakeInvites()
	svc := newTestService(repo, invites, pol)
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/self/register", h.RegisterSelf)
	// The Me session resolves the principal lazily per request so the
	// test can register first and read back afterwards.
	mux.HandleFunc("GET /v1/self/me", func(w http.ResponseWriter, r *http.Request) {
		info := authnport.TokenInfo{PrincipalID: firstPrincipalID(repo), Roles: []string{"member"}}
		h.Me(w, r.WithContext(authnport.WithTokenInfo(r.Context(), info)))
	})
	mux.HandleFunc("POST /v1/admin/principals", h.ProvisionAdmin)
	mux.HandleFunc("GET /v1/admin/principals", h.ListAdmin)
	mux.HandleFunc("GET /v1/admin/principals/{id}", h.GetAdmin)
	mux.HandleFunc("POST /v1/admin/principals/{id}/approve", h.ApproveAdmin)
	mux.HandleFunc("POST /v1/admin/principals/{id}/status", h.SetStatusAdmin)
	mux.HandleFunc("POST /v1/admin/invites", asSession(authnport.TokenInfo{
		PrincipalID: "admin-1",
		Roles:       []string{"admin"},
	}, http.HandlerFunc(h.CreateInviteAdmin)).ServeHTTP)
	mux.HandleFunc("POST /v1/bootstrap/invite", h.CreateBootstrapInvite)
	return mux, invites
}

func firstPrincipalID(repo *fakeRepo) string {
	for id := range repo.byID {
		return id
	}
	return ""
}

func asSession(info authnport.TokenInfo, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(authnport.WithTokenInfo(r.Context(), info)))
	})
}

func do(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response must be JSON, got %q: %v", rec.Body.String(), err)
	}
	return out
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := decodeBody(t, rec)
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error response must carry the error object, got %v", body)
	}
	code, _ := errObj["code"].(string)
	return code
}

const goodPassword = "correct horse battery"

func TestHTTPRegisterSelfRegister(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeSelfRegisterWithApproval))
	rec := do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"ker","password":"`+goodPassword+`","display_name":"Ker"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["status"] != "pending" {
		t.Fatalf("self-register must start pending, got %v", body)
	}
	if body["id"] == "" || body["id"] == nil {
		t.Fatalf("response must carry the new id, got %v", body)
	}
}

func TestHTTPRegisterAdminOnlyForbidden(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"ker","password":"`+goodPassword+`"}`)
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "registration_forbidden" {
		t.Fatalf("want 403 registration_forbidden, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPRegisterValidationErrors(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		code  int
		eCode string
	}{
		{"short password", `{"username":"ker","password":"short"}`, 400, "invalid_request"},
		{"bad username", `{"username":"K!","password":"` + goodPassword + `"}`, 400, "invalid_request"},
		{"malformed json", `{"username":`, 400, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := newTestMux(policyOf(policydomain.ModeSelfRegisterWithApproval))
			rec := do(t, mux, http.MethodPost, "/v1/self/register", tc.body)
			if rec.Code != tc.code || errCode(t, rec) != tc.eCode {
				t.Fatalf("want %d %q, got %d %q", tc.code, tc.eCode, rec.Code, errCode(t, rec))
			}
		})
	}
}

func TestHTTPRegisterUsernameTaken(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeSelfRegisterWithApproval))
	body := `{"username":"ker","password":"` + goodPassword + `"}`
	if rec := do(t, mux, http.MethodPost, "/v1/self/register", body); rec.Code != http.StatusCreated {
		t.Fatalf("first register: %d", rec.Code)
	}
	rec := do(t, mux, http.MethodPost, "/v1/self/register", body)
	if rec.Code != http.StatusConflict || errCode(t, rec) != "username_taken" {
		t.Fatalf("want 409 username_taken, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPMe(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeSelfRegisterWithApproval))
	rec := do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"ker","password":"`+goodPassword+`","display_name":"Ker"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d", rec.Code)
	}
	rec = do(t, mux, http.MethodGet, "/v1/self/me", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("me: %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	for _, key := range []string{"id", "username", "kind", "status", "roles", "display_name"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("me response must carry %q, got %v", key, body)
		}
	}
	if _, leaked := body["password_hash"]; leaked {
		t.Fatal("password_hash must never be northbound")
	}
}

func TestHTTPMeUnknownSessionPrincipal(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	// The Me stub resolves the first principal in the repo; with an
	// empty repo that id matches nothing.
	rec := do(t, mux, http.MethodGet, "/v1/self/me", "")
	if rec.Code != http.StatusNotFound || errCode(t, rec) != "not_found" {
		t.Fatalf("want 404 not_found for a session whose principal vanished, got %d %q",
			rec.Code, errCode(t, rec))
	}
}

func TestHTTPMeHandlerWithoutSession(t *testing.T) {
	// Mounted raw (no middleware): the handler itself must fail closed.
	repo := newFakeRepo()
	svc := newTestService(repo, newFakeInvites(), policyOf(policydomain.ModeAdminOnly))
	h := NewHandler(svc)
	rec := httptest.NewRecorder()
	h.Me(rec, httptest.NewRequest(http.MethodGet, "/v1/self/me", nil))
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthenticated" {
		t.Fatalf("want 401 unauthenticated, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPProvisionAdmin(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/admin/principals",
		`{"username":"svc-bot","password":"`+goodPassword+`","display_name":"Bot","kind":"service"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["kind"] != "service" || body["status"] != "active" {
		t.Fatalf("provisioned service principal must be active, got %v", body)
	}
}

func TestHTTPProvisionBadKind(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/admin/principals",
		`{"username":"bot","password":"`+goodPassword+`","kind":"bot"}`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_request" {
		t.Fatalf("want 400 invalid_request, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPProvisionWithRoles(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/admin/principals",
		`{"username":"root","password":"`+goodPassword+`","kind":"human","roles":["admin"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	if diff := cmp.Diff([]any{"admin"}, decodeBody(t, rec)["roles"]); diff != "" {
		t.Fatalf("roles mismatch (-want +got):\n%s", diff)
	}
}

func TestHTTPListAndGetAdmin(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	for _, u := range []string{"u-one", "u-two"} {
		rec := do(t, mux, http.MethodPost, "/v1/admin/principals",
			`{"username":"`+u+`","password":"`+goodPassword+`","kind":"human"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("provision %s: %d", u, rec.Code)
		}
	}
	rec := do(t, mux, http.MethodGet, "/v1/admin/principals?limit=1&offset=0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	body := decodeBody(t, rec)
	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("limit=1 must return one item, got %v", body)
	}
	if body["total"] != float64(2) {
		t.Fatalf("total must report both principals, got %v", body["total"])
	}
	first := items[0].(map[string]any)
	id, _ := first["id"].(string)
	rec = do(t, mux, http.MethodGet, "/v1/admin/principals/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	rec = do(t, mux, http.MethodGet, "/v1/admin/principals/nope", "")
	if rec.Code != http.StatusNotFound || errCode(t, rec) != "not_found" {
		t.Fatalf("want 404 not_found, got %d %q", rec.Code, errCode(t, rec))
	}
	// Garbage paging params fall back to the defaults, still 200.
	rec = do(t, mux, http.MethodGet, "/v1/admin/principals?limit=abc&offset=-3", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list with garbage params: %d", rec.Code)
	}
}

func TestHTTPApproveAndStatusAdmin(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeSelfRegisterWithApproval))
	rec := do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"ker","password":"`+goodPassword+`"}`)
	id, _ := decodeBody(t, rec)["id"].(string)

	rec = do(t, mux, http.MethodPost, "/v1/admin/principals/"+id+"/approve", "")
	if rec.Code != http.StatusOK || decodeBody(t, rec)["status"] != "active" {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, http.MethodPost, "/v1/admin/principals/"+id+"/approve", "")
	if rec.Code != http.StatusConflict || errCode(t, rec) != "invalid_transition" {
		t.Fatalf("double approve must be invalid_transition, got %d %q", rec.Code, errCode(t, rec))
	}

	rec = do(t, mux, http.MethodPost, "/v1/admin/principals/"+id+"/status", `{"status":"disabled"}`)
	if rec.Code != http.StatusOK || decodeBody(t, rec)["status"] != "disabled" {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, http.MethodPost, "/v1/admin/principals/"+id+"/status", `{"status":"active"}`)
	if rec.Code != http.StatusOK || decodeBody(t, rec)["status"] != "active" {
		t.Fatalf("re-enable: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, http.MethodPost, "/v1/admin/principals/"+id+"/status", `{"status":"pending"}`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_status" {
		t.Fatalf("pending target must be rejected, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPDisableLastAdminRejected(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/admin/principals",
		`{"username":"root","password":"`+goodPassword+`","kind":"human","roles":["admin"]}`)
	id, _ := decodeBody(t, rec)["id"].(string)

	rec = do(t, mux, http.MethodPost, "/v1/admin/principals/"+id+"/status", `{"status":"disabled"}`)
	if rec.Code != http.StatusConflict || errCode(t, rec) != "last_admin" {
		t.Fatalf("want 409 last_admin, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPInviteErrorIsGeneric(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeInvite))
	rec := do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"ker","password":"`+goodPassword+`","invite_token":"bogus"}`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_invite" {
		t.Fatalf("want 400 invalid_invite, got %d %q", rec.Code, errCode(t, rec))
	}
	body := decodeBody(t, rec)
	errObj := body["error"].(map[string]any)
	if errObj["message"] != "invalid_invite" {
		t.Fatalf("invite failure must not leak internals, got message %q", errObj["message"])
	}
}

func TestHTTPCreateInviteAdmin(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/admin/invites", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	token, _ := body["token"].(string)
	expires, _ := body["expires_at"].(string)
	if token == "" || expires == "" {
		t.Fatalf("invite response must carry token and expires_at, got %v", body)
	}
	if _, err := time.Parse(time.RFC3339, expires); err != nil {
		t.Fatalf("expires_at must be RFC3339, got %q", expires)
	}

	rec = do(t, mux, http.MethodPost, "/v1/admin/invites", `{"ttl":"nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad ttl must be rejected, got %d", rec.Code)
	}

	rec = do(t, mux, http.MethodPost, "/v1/admin/invites", `{"ttl":"2h"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("explicit ttl must be accepted, got %d (%s)", rec.Code, rec.Body.String())
	}
	expires, _ = decodeBody(t, rec)["expires_at"].(string)
	if parsed, err := time.Parse(time.RFC3339, expires); err != nil || !parsed.Equal(testNow.Add(2*time.Hour)) {
		t.Fatalf("explicit ttl must drive expires_at, got %q", expires)
	}
}

func TestHTTPInviteConsumedByRegistration(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeInvite))
	rec := do(t, mux, http.MethodPost, "/v1/admin/invites", "")
	token, _ := decodeBody(t, rec)["token"].(string)
	if token == "" {
		t.Fatal("invite must return a raw token")
	}
	rec = do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"ker","password":"`+goodPassword+`","invite_token":"`+token+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register with invite: %d (%s)", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"bob","password":"`+goodPassword+`","invite_token":"`+token+`"}`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_invite" {
		t.Fatalf("consumed invite must be invalid_invite, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPCreateBootstrapInvite(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/bootstrap/invite", `{"email":"ops@example.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	token, _ := body["token"].(string)
	expires, _ := body["expires_at"].(string)
	path, _ := body["invite_url_path"].(string)
	if token == "" || expires == "" {
		t.Fatalf("bootstrap invite response must carry token and expires_at, got %v", body)
	}
	if _, err := time.Parse(time.RFC3339, expires); err != nil {
		t.Fatalf("expires_at must be RFC3339, got %q", expires)
	}
	if path != "/v1/self/register" {
		t.Fatalf("invite_url_path must name the registration endpoint, got %q", path)
	}

	// The one-shot window closes after the first mint.
	rec = do(t, mux, http.MethodPost, "/v1/bootstrap/invite", `{"email":"second@example.com"}`)
	if rec.Code != http.StatusConflict || errCode(t, rec) != "bootstrap_closed" {
		t.Fatalf("second bootstrap invite must be 409 bootstrap_closed, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPCreateBootstrapInviteRejectsBadEmail(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	for _, body := range []string{`{}`, `{"email":""}`, `{"email":"nope"}`} {
		rec := do(t, mux, http.MethodPost, "/v1/bootstrap/invite", body)
		if rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_request" {
			t.Fatalf("body %s: want 400 invalid_request, got %d %q", body, rec.Code, errCode(t, rec))
		}
	}
}

func TestHTTPBootstrapInviteClosedOnceAdminExists(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/admin/principals",
		`{"username":"root","password":"`+goodPassword+`","kind":"human","roles":["admin"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("provision admin: %d (%s)", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, http.MethodPost, "/v1/bootstrap/invite", `{"email":"ops@example.com"}`)
	if rec.Code != http.StatusConflict || errCode(t, rec) != "bootstrap_closed" {
		t.Fatalf("want 409 bootstrap_closed, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPRegisterWithBootstrapInviteUnderAdminOnly(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/bootstrap/invite", `{"email":"ops@example.com"}`)
	token, _ := decodeBody(t, rec)["token"].(string)
	if token == "" {
		t.Fatalf("bootstrap invite must return a raw token, got %s", rec.Body.String())
	}

	rec = do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"root","password":"`+goodPassword+`","invite_token":"`+token+`","email":"ops@example.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bootstrap registration under admin-only must succeed: %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["status"] != "active" {
		t.Fatalf("bootstrap registration starts active, got %v", body)
	}
	// The register response is minimal by contract; the principal view
	// carries the granted roles.
	rec = do(t, mux, http.MethodGet, "/v1/self/me", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("me after bootstrap registration: %d (%s)", rec.Code, rec.Body.String())
	}
	if diff := cmp.Diff([]any{"admin", "member"}, decodeBody(t, rec)["roles"]); diff != "" {
		t.Fatalf("bootstrap registration grants admin+member (-want +got):\n%s", diff)
	}

	// One-time: the same voucher never registers a second principal.
	rec = do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"root2","password":"`+goodPassword+`","invite_token":"`+token+`","email":"ops@example.com"}`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_invite" {
		t.Fatalf("consumed bootstrap invite must be invalid_invite, got %d %q", rec.Code, errCode(t, rec))
	}
}

func TestHTTPRegisterBootstrapInviteEmailMismatch(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/bootstrap/invite", `{"email":"ops@example.com"}`)
	token, _ := decodeBody(t, rec)["token"].(string)
	if token == "" {
		t.Fatalf("bootstrap invite must return a raw token, got %s", rec.Body.String())
	}
	rec = do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"root","password":"`+goodPassword+`","invite_token":"`+token+`","email":"other@example.com"}`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_request" {
		t.Fatalf("email mismatch must be 400 invalid_request, got %d %q", rec.Code, errCode(t, rec))
	}
	// The voucher survives the rejected attempt.
	rec = do(t, mux, http.MethodPost, "/v1/self/register",
		`{"username":"root","password":"`+goodPassword+`","invite_token":"`+token+`","email":"ops@example.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registration with the bound email must succeed after a mismatch: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestHTTPPrincipalViewShape(t *testing.T) {
	mux, _ := newTestMux(policyOf(policydomain.ModeAdminOnly))
	rec := do(t, mux, http.MethodPost, "/v1/admin/principals",
		`{"username":"ker","password":"`+goodPassword+`","kind":"human"}`)
	body := decodeBody(t, rec)
	wantKeys := []string{"id", "username", "kind", "display_name", "status", "roles", "version", "created_at", "updated_at"}
	for _, k := range wantKeys {
		if _, ok := body[k]; !ok {
			t.Fatalf("principal view must carry %q, got keys %v", k, bodyKeys(body))
		}
	}
	if diff := cmp.Diff([]any{"member"}, body["roles"]); diff != "" {
		t.Fatalf("default roles mismatch (-want +got):\n%s", diff)
	}
}

func bodyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
