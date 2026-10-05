// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"errors"
	"testing"
	"time"
)

func TestModeValid(t *testing.T) {
	for _, m := range []Mode{ModeAdminOnly, ModeInvite, ModeSelfRegisterWithApproval} {
		if !m.Valid() {
			t.Fatalf("%q must be valid", m)
		}
	}
	if Mode("anything-goes").Valid() {
		t.Fatal("unknown mode must be invalid")
	}
}

func TestNewPolicy(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	p, err := New(ModeInvite, "admin-1", now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.ID != SingletonID {
		t.Fatalf("singleton id must be %q, got %q", SingletonID, p.ID)
	}
	if p.Mode != ModeInvite || p.UpdatedBy != "admin-1" || !p.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected policy: %+v", p)
	}
}

func TestNewPolicyRejectsBadMode(t *testing.T) {
	_, err := New(Mode("open-season"), "admin-1", time.Now())
	if !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("want ErrInvalidMode, got %v", err)
	}
}
