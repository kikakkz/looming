// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kikakkz/looming/identity/internal/authn/domain"
	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
)

type middlewareStub struct {
	info authnport.TokenInfo
	err  error
	saw  string
}

func (m *middlewareStub) VerifyPassword(context.Context, string, string) (string, error) {
	return "", nil
}

func (m *middlewareStub) Issue(context.Context, string) (string, error) { return "", nil }

func (m *middlewareStub) Validate(_ context.Context, raw string) (authnport.TokenInfo, error) {
	m.saw = raw
	return m.info, m.err
}

func (m *middlewareStub) Revoke(context.Context, string) error { return nil }

func TestRequireAuthHappyPath(t *testing.T) {
	stub := &middlewareStub{info: authnport.TokenInfo{
		PrincipalID: "p-1", Roles: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour),
	}}
	var got context.Context
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Context()
		w.WriteHeader(http.StatusNoContent)
	})
	handler := requireAuth(stub, true, next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer raw-token")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("want pass-through 204, got %d", resp.Code)
	}
	if stub.saw != "raw-token" {
		t.Fatalf("provider must see the raw token, got %q", stub.saw)
	}
	info, ok := authnport.TokenInfoFrom(got)
	if !ok || info.PrincipalID != "p-1" {
		t.Fatalf("claims must reach the handler via context, got %+v ok=%v", info, ok)
	}
}

func TestRequireAuthRejectsMissingBearer(t *testing.T) {
	handler := requireAuth(&middlewareStub{}, false,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/", nil))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.Code)
	}
}

func TestRequireAuthRejectsMalformedHeaders(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"basic scheme", "Basic dXNlcjpwYXNz"},
		{"raw token without scheme", "sometoken"},
		{"bare Bearer", "Bearer"},
		{"empty token", "Bearer "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := requireAuth(&middlewareStub{}, false,
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", tc.value)
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d", resp.Code)
			}
		})
	}
}

func TestRequireAuthMapsValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"unknown token", domain.ErrInvalidCredential, "unauthenticated"},
		{"expired", domain.ErrTokenExpired, "token_expired"},
		{"revoked", domain.ErrTokenRevoked, "token_revoked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &middlewareStub{err: tc.err}
			handler := requireAuth(stub, false,
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer x")
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d", resp.Code)
			}
		})
	}
}

func TestRequireAuthAdminRoleEnforcement(t *testing.T) {
	cases := []struct {
		name     string
		roles    []string
		admin    bool
		wantCode int
	}{
		{"admin allowed on admin route", []string{"admin"}, true, http.StatusNoContent},
		{"member denied on admin route", []string{"member"}, true, http.StatusForbidden},
		{"member allowed on self route", []string{"member"}, false, http.StatusNoContent},
		{"no roles denied on admin route", nil, true, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &middlewareStub{info: authnport.TokenInfo{
				PrincipalID: "p-1", Roles: tc.roles, ExpiresAt: time.Now().Add(time.Hour),
			}}
			handler := requireAuth(stub, tc.admin,
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer x")
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != tc.wantCode {
				t.Fatalf("want %d, got %d", tc.wantCode, resp.Code)
			}
		})
	}
}

func TestArgonLimitRejectsWhenSaturated(t *testing.T) {
	limit := newArgonLimit(1)
	reached := make(chan struct{})
	release := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(reached)
		<-release // hold the permit until the test releases it
		w.WriteHeader(http.StatusNoContent)
	})
	handler := limit.wrap(next)
	firstDone := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		<-firstDone
	})

	// In-process only: the unit layer admits no network (AD-25), so
	// the wrapped handler is driven with ServeHTTP, not a test server.
	go func() {
		defer close(firstDone)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-reached // the single permit is now held

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/", nil))
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 busy when saturated, got %d", resp.Code)
	}
}

func TestArgonLimitRejectsInvalidConfig(t *testing.T) {
	limit := newArgonLimit(0)
	if cap(limit.permits) != defaultArgonConcurrency {
		t.Fatalf("non-positive size must fall back to %d, got %d", defaultArgonConcurrency, cap(limit.permits))
	}
}

func TestRequireBootstrapKey(t *testing.T) {
	passThrough := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	cases := []struct {
		name     string
		key      string
		header   string
		wantCode int
	}{
		{"unconfigured key reports disabled", "", "Bootstrap x", http.StatusServiceUnavailable},
		{"correct key passes", "s3cret-key", "Bootstrap s3cret-key", http.StatusNoContent},
		{"wrong key rejected", "s3cret-key", "Bootstrap nope", http.StatusUnauthorized},
		{"bearer scheme rejected", "s3cret-key", "Bearer s3cret-key", http.StatusUnauthorized},
		{"bare scheme rejected", "s3cret-key", "Bootstrap", http.StatusUnauthorized},
		{"empty credential rejected", "s3cret-key", "Bootstrap ", http.StatusUnauthorized},
		{"missing header rejected", "s3cret-key", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := requireBootstrapKey(tc.key, passThrough)
			req := httptest.NewRequest(http.MethodPost, "/v1/bootstrap/invite", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != tc.wantCode {
				t.Fatalf("want %d, got %d", tc.wantCode, resp.Code)
			}
			if tc.wantCode == http.StatusServiceUnavailable {
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
					t.Fatalf("disabled response must be JSON: %v", err)
				}
				if body.Error.Code != "bootstrap_disabled" {
					t.Fatalf("want bootstrap_disabled, got %q", body.Error.Code)
				}
			}
		})
	}
}
