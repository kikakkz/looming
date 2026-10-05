// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
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
	srv := httptest.NewServer(requireAuth(stub, true, next))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer raw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want pass-through 204, got %d", resp.StatusCode)
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
	srv := httptest.NewServer(requireAuth(&middlewareStub{}, false,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
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
			srv := httptest.NewServer(requireAuth(stub, false,
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			req.Header.Set("Authorization", "Bearer x")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d", resp.StatusCode)
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
			srv := httptest.NewServer(requireAuth(stub, tc.admin,
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				})))
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			req.Header.Set("Authorization", "Bearer x")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("want %d, got %d", tc.wantCode, resp.StatusCode)
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

	srv := httptest.NewServer(limit.wrap(next))
	defer srv.Close()

	go func() {
		resp, err := http.Get(srv.URL)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-reached // the single permit is now held

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 busy when saturated, got %d", resp.StatusCode)
	}

	close(release) // first request finishes and frees its permit
}

func TestArgonLimitRejectsInvalidConfig(t *testing.T) {
	limit := newArgonLimit(0)
	if cap(limit.permits) != defaultArgonConcurrency {
		t.Fatalf("non-positive size must fall back to %d, got %d", defaultArgonConcurrency, cap(limit.permits))
	}
}
