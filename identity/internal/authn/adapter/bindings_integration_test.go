// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kikakkz/looming/identity/internal/authn/adapter"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	principaladapter "github.com/kikakkz/looming/identity/internal/principal/adapter"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

func newBoundPrincipal(t *testing.T) *principaldomain.Principal {
	t.Helper()
	p, err := principaldomain.NewRegistration(
		uuid.NewString(), "bound-"+uuid.NewString()[:8], principaldomain.KindHuman,
		"Bound", "", principaldomain.StatusActive, time.Now())
	if err != nil {
		t.Fatalf("NewRegistration: %v", err)
	}
	return p
}

func TestBindingRepositoryRoundTrip(t *testing.T) {
	db := pgtest.NewDB(t)
	repo := principaladapter.NewRepository(db)
	bindings := adapter.NewBindingRepository(db)
	ctx := context.Background()

	p := newBoundPrincipal(t)
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create principal: %v", err)
	}

	if _, err := bindings.ByIssuerSubject(ctx, "https://idp.example", "sub-1"); !errors.Is(err, principaldomain.ErrNotFound) {
		t.Fatalf("unknown binding must be ErrNotFound, got %v", err)
	}
	if err := bindings.Create(ctx, "https://idp.example", "sub-1", p.ID); err != nil {
		t.Fatalf("Create binding: %v", err)
	}
	got, err := bindings.ByIssuerSubject(ctx, "https://idp.example", "sub-1")
	if err != nil {
		t.Fatalf("ByIssuerSubject: %v", err)
	}
	if got != p.ID {
		t.Fatalf("want %q, got %q", p.ID, got)
	}
	// A second pair for the same principal is fine; the same pair is not.
	if err := bindings.Create(ctx, "https://idp.example", "sub-2", p.ID); err != nil {
		t.Fatalf("second subject binding: %v", err)
	}
	if err := bindings.Create(ctx, "https://idp.example", "sub-1", p.ID); err == nil {
		t.Fatalf("duplicate binding pair must fail")
	}
}
