// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kikakkz/looming/internal/engineplane"
	frontdomain "github.com/kikakkz/looming/internal/front/domain"
	"go.uber.org/goleak"
)

type stubAuthn struct{ subject string }

func (s stubAuthn) Authenticate(_ context.Context, key string) (string, error) {
	if key == "good-key" {
		return s.subject, nil
	}
	return "", errors.New("bad key")
}

type frontTestLink struct{ v frontdomain.Verdict }

func (s frontTestLink) Evaluate(_ context.Context, _ frontdomain.LinkContext, _ *http.Request) (frontdomain.Verdict, error) {
	return s.v, nil
}

type stubAllowlist struct{}

func (stubAllowlist) Models(_ context.Context, _ string) ([]string, error) {
	return []string{"gpt-5"}, nil
}

type stubEngine struct{ body string }

func (s stubEngine) Forward(_ context.Context, w http.ResponseWriter, _ *http.Request) error {
	w.WriteHeader(http.StatusOK)
	_, err := io.WriteString(w, s.body)
	return err
}

func newTestFront(engineBody string) *Front {
	return NewFront(
		stubAuthn{subject: "ker"},
		stubAllowlist{},
		frontdomain.NewChain(frontTestLink{v: frontdomain.VerdictPass}),
		stubEngine{body: engineBody},
		func(r *http.Request) string { return r.Header.Get("X-Test-Model") },
		nil,
	)
}

func TestFrontPipelinePass(t *testing.T) {
	defer goleak.VerifyNone(t)
	front := newTestFront("ok")
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer good-key")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("want 200 ok, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestFrontRejectsBadKey(t *testing.T) {
	defer goleak.VerifyNone(t)
	front := newTestFront("ok")
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer nope")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestFrontRejectsUnlistedModel(t *testing.T) {
	defer goleak.VerifyNone(t)
	front := newTestFront("ok")
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer good-key")
	req.Header.Set("X-Test-Model", "claude-3")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("authenticated model denial is 403, got %d", rec.Code)
	}
}

func TestFrontEngineFailureIsBadGateway(t *testing.T) {
	defer goleak.VerifyNone(t)
	broken := stubEngineFunc(func(http.ResponseWriter, *http.Request) error {
		return engineplane.ErrNotImplemented
	})
	front := NewFront(stubAuthn{subject: "ker"}, stubAllowlist{},
		frontdomain.NewChain(frontTestLink{v: frontdomain.VerdictPass}), broken,
		func(r *http.Request) string { return r.Header.Get("X-Test-Model") }, nil)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer good-key")
	req.Header.Set("X-Test-Model", "gpt-5")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
}

type stubEngineFunc func(http.ResponseWriter, *http.Request) error

func (s stubEngineFunc) Forward(_ context.Context, w http.ResponseWriter, r *http.Request) error {
	return s(w, r)
}
