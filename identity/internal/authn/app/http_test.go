// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authndomain "github.com/kikakkz/looming/identity/internal/authn/domain"
	"github.com/kikakkz/looming/identity/internal/authn/port"
)

type stubProvider struct {
	info        port.TokenInfo
	validateErr error
	verifyErr   error
	raw         string
}

func (s *stubProvider) VerifyPassword(context.Context, string, string) (string, error) {
	if s.verifyErr != nil {
		return "", s.verifyErr
	}
	return "p-1", nil
}

func (s *stubProvider) VerifyExternalToken(context.Context, string) (string, error) {
	return "", authndomain.ErrExternalAuthnNotSupported
}

func (s *stubProvider) Issue(context.Context, string) (string, error) { return s.raw, nil }

func (s *stubProvider) Validate(context.Context, string) (port.TokenInfo, error) {
	return s.info, s.validateErr
}

func (s *stubProvider) Revoke(context.Context, string) error { return nil }

func newLoginMux(prov port.Provider) *http.ServeMux {
	login := NewLoginService(prov, time.Hour, func() time.Time {
		return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/self/login", NewHandler(login).Login)
	return mux
}

func postLogin(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/self/login", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHTTPLoginSuccess(t *testing.T) {
	mux := newLoginMux(&stubProvider{raw: "raw-token"})
	rec := postLogin(t, mux, `{"username":"ker","password":"whatever-fine"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"token":"raw-token"`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"expires_at":"2026-10-05T13:00:00Z"`)) {
		t.Fatalf("response must carry token and expiry, got %s", rec.Body.String())
	}
}

func TestHTTPLoginInvalidCredentials(t *testing.T) {
	mux := newLoginMux(&stubProvider{verifyErr: authndomain.ErrInvalidCredential})
	rec := postLogin(t, mux, `{"username":"ker","password":"wrong"}`)
	if rec.Code != http.StatusUnauthorized ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"code":"invalid_credentials"`)) {
		t.Fatalf("want 401 invalid_credentials, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPLoginMalformedBody(t *testing.T) {
	mux := newLoginMux(&stubProvider{raw: "x"})
	rec := postLogin(t, mux, `{"username":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestHTTPLoginInternalFailure(t *testing.T) {
	mux := newLoginMux(&stubProvider{verifyErr: errors.New("db down")})
	rec := postLogin(t, mux, `{"username":"ker","password":"whatever-fine"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
}
