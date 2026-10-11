// SPDX-License-Identifier: Apache-2.0

package identityclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newFake(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestLoginHappyPath(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/self/login" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token": "tok-1", "expires_at": "2027-01-01T00:00:00Z",
		})
	})
	session, err := c.Login(context.Background(), "ker", "pw")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if session.Token != "tok-1" {
		t.Fatalf("token: %+v", session)
	}
}

func TestLoginExternalAndErrorCodes(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["id_token"] != "" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token": "tok-oidc", "expires_at": "2027-01-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "invalid_credentials", "message": "nope"},
		})
	})
	session, err := c.LoginExternal(context.Background(), "id-tok")
	if err != nil || session.Token != "tok-oidc" {
		t.Fatalf("LoginExternal: %v %+v", err, session)
	}
	_, err = c.Login(context.Background(), "ker", "wrong")
	if !IsCode(err, "invalid_credentials") {
		t.Fatalf("want coded error, got %v", err)
	}
	var apiErr *Error
	if !asError(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error shape: %v", err)
	}
}

func asError(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestIssueKeySendsBearer(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "k-1", "key": "lk-raw"})
	})
	key, err := c.IssueKey(context.Background(), &Session{Token: "session"}, "laptop")
	if err != nil || key.Key != "lk-raw" {
		t.Fatalf("IssueKey: %v %+v", err, key)
	}
}

func TestSelfQuotaNotSet(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "no_quota"},
		})
	})
	quota, err := c.SelfQuota(context.Background(), &Session{Token: "session"})
	if err != nil {
		t.Fatalf("SelfQuota: %v", err)
	}
	if quota.Exists {
		t.Fatalf("no_quota must surface Exists=false")
	}
}

func TestSelfQuotaValue(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"amount": 500, "unit": "usd", "window_days": 30,
		})
	})
	quota, err := c.SelfQuota(context.Background(), &Session{Token: "s"})
	if err != nil {
		t.Fatalf("SelfQuota: %v", err)
	}
	if !quota.Exists || quota.Quota.Amount != 500 || quota.Quota.Unit != "usd" {
		t.Fatalf("quota: %+v", quota)
	}
}

// TestProvisionServicePrincipalPostsKindService: #143 stage 2's service
// identity rides identity's existing admin surface — the request
// carries the admin bearer and kind=service, the response exposes the
// new principal's id (the password hash never leaves the service).
func TestProvisionServicePrincipalPostsKindService(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/principals" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer admin-session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["kind"] != "service" || body["username"] != "looming-advisor" || body["password"] == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": "p-1", "username": "looming-advisor", "kind": "service", "status": "active",
		})
	})
	p, err := c.ProvisionServicePrincipal(context.Background(), "admin-session", "looming-advisor", "bootstrap-pw-123456", "Looming management agent")
	if err != nil {
		t.Fatalf("ProvisionServicePrincipal: %v", err)
	}
	if p.ID != "p-1" || p.Kind != "service" || p.Status != "active" {
		t.Fatalf("principal shape: %+v", p)
	}
}

// TestProvisionServicePrincipalUsernameTaken: the idempotency signal —
// a repeat provision surfaces identity's 409 username_taken so the
// caller can branch into its recovery path.
func TestProvisionServicePrincipalUsernameTaken(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "username_taken", "message": "username looming-advisor is taken"},
		})
	})
	_, err := c.ProvisionServicePrincipal(context.Background(), "admin-session", "looming-advisor", "bootstrap-pw-123456", "x")
	if !IsCode(err, "username_taken") {
		t.Fatalf("want username_taken, got %v", err)
	}
}
