// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/kikakkz/looming/identity/internal/policy/domain"
)

type fakeStore struct {
	policy *domain.Policy
	err    error
	setErr error
	set    []*domain.Policy
}

func (f *fakeStore) Get(context.Context) (*domain.Policy, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.policy == nil {
		return nil, domain.ErrNotFound
	}
	return f.policy, nil
}

func (f *fakeStore) Set(_ context.Context, p *domain.Policy) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.set = append(f.set, p)
	return nil
}

func TestGetReturnsStoredPolicy(t *testing.T) {
	now := time.Now()
	svc := NewService(&fakeStore{policy: &domain.Policy{
		ID: domain.SingletonID, Mode: domain.ModeInvite, UpdatedBy: "a", UpdatedAt: now,
	}}, func() time.Time { return now })
	p, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Mode != domain.ModeInvite {
		t.Fatalf("want invite, got %s", p.Mode)
	}
}

func TestGetDefaultsToAdminOnlyWhenUnset(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	svc := NewService(&fakeStore{}, func() time.Time { return now })
	p, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Mode != domain.ModeAdminOnly {
		t.Fatalf("empty policy table must fail closed to admin-only, got %s", p.Mode)
	}
}

func TestGetPropagatesStoreFailure(t *testing.T) {
	svc := NewService(&fakeStore{err: errors.New("db down")}, time.Now)
	if _, err := svc.Get(context.Background()); err == nil {
		t.Fatal("store failure must propagate (registration fails closed on it)")
	}
}

func TestSetWritesSingleton(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	svc := NewService(store, func() time.Time { return now })
	p, err := svc.Set(context.Background(), domain.ModeSelfRegisterWithApproval, "admin-9")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := &domain.Policy{
		ID: domain.SingletonID, Mode: domain.ModeSelfRegisterWithApproval,
		UpdatedBy: "admin-9", UpdatedAt: now,
	}
	if diff := cmp.Diff(want, p); diff != "" {
		t.Fatalf("mismatch (-want +got):\n%s", diff)
	}
	if len(store.set) != 1 {
		t.Fatalf("store must receive exactly one write, got %d", len(store.set))
	}
}

func TestSetRejectsInvalidMode(t *testing.T) {
	svc := NewService(&fakeStore{}, time.Now)
	_, err := svc.Set(context.Background(), domain.Mode("yolo"), "admin-9")
	if !errors.Is(err, domain.ErrInvalidMode) {
		t.Fatalf("want ErrInvalidMode, got %v", err)
	}
}
