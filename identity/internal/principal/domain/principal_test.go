// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestValidUsername(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"simple", "ker", true},
		{"digits and separators", "a.b-c_d9", true},
		{"max length", "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijij", true},
		{"too short", "ab", false},
		{"too long", "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijij1", false},
		{"uppercase", "Ker", false},
		{"leading separator", ".ker", false},
		{"trailing separator", "ker-", false},
		{"empty", "", false},
		{"space", "k e r", false},
		{"leading digit ok", "9ker", true},
		{"inner double separator ok", "a--b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidUsername(tc.in); got != tc.want {
				t.Fatalf("ValidUsername(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewRegistrationDefaults(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	p, err := NewRegistration("id-1", "ker", KindHuman, "Ker", "hash", StatusPending, now)
	if err != nil {
		t.Fatalf("NewRegistration: %v", err)
	}
	want := &Principal{
		ID:           "id-1",
		Username:     "ker",
		Kind:         KindHuman,
		DisplayName:  "Ker",
		PasswordHash: "hash",
		Status:       StatusPending,
		Roles:        []string{RoleMember},
		Version:      1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if diff := cmp.Diff(want, p); diff != "" {
		t.Fatalf("mismatch (-want +got):\n%s", diff)
	}
}

func TestNewRegistrationWithRoles(t *testing.T) {
	now := time.Now()
	p, err := NewRegistration("id-1", "root", KindHuman, "", "", StatusActive, now, WithRoles([]string{RoleAdmin}))
	if err != nil {
		t.Fatalf("NewRegistration: %v", err)
	}
	if diff := cmp.Diff([]string{RoleAdmin}, p.Roles); diff != "" {
		t.Fatalf("roles mismatch (-want +got):\n%s", diff)
	}
	if p.Status != StatusActive {
		t.Fatalf("want active, got %s", p.Status)
	}
}

func TestNewRegistrationRejectsBadInput(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		id       string
		username string
		kind     Kind
		display  string
		status   Status
		wantErr  error
	}{
		{"bad username", "id", "K!", KindHuman, "", StatusPending, ErrInvalidUsername},
		{"bad kind", "id", "ker", Kind("bot"), "", StatusPending, ErrInvalidKind},
		{"bad initial status", "id", "ker", KindHuman, "", StatusDisabled, ErrInvalidStatus},
		{"long display name", "id", "ker", KindHuman, string(make([]byte, 129)), StatusPending, ErrDisplayNameTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRegistration(tc.id, tc.username, tc.kind, tc.display, "", tc.status, now)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestStatusTransitions(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Minute)

	cases := []struct {
		name    string
		from    Status
		op      func(*Principal, time.Time) error
		want    Status
		wantErr bool
	}{
		{"pending approve", StatusPending, (*Principal).Approve, StatusActive, false},
		{"pending disable", StatusPending, (*Principal).Disable, StatusDisabled, false},
		{"active disable", StatusActive, (*Principal).Disable, StatusDisabled, false},
		{"disabled re-enable", StatusDisabled, (*Principal).Activate, StatusActive, false},
		{"active approve rejected", StatusActive, (*Principal).Approve, StatusActive, true},
		{"disabled approve rejected", StatusDisabled, (*Principal).Approve, StatusDisabled, true},
		{"pending activate rejected", StatusPending, (*Principal).Activate, StatusPending, true},
		{"active activate rejected", StatusActive, (*Principal).Activate, StatusActive, true},
		{"disabled disable rejected", StatusDisabled, (*Principal).Disable, StatusDisabled, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Principal{Status: tc.from}
			err := tc.op(p, later)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("want ErrInvalidTransition, got %v", err)
				}
				if p.Status != tc.from {
					t.Fatalf("rejected transition must not mutate status, got %s", p.Status)
				}
				return
			}
			if err != nil {
				t.Fatalf("op: %v", err)
			}
			if p.Status != tc.want {
				t.Fatalf("want %s, got %s", tc.want, p.Status)
			}
			if !p.UpdatedAt.Equal(later) {
				t.Fatalf("UpdatedAt must move to the injected clock: %s", p.UpdatedAt)
			}
		})
	}
}

func TestSetStatusDispatch(t *testing.T) {
	now := time.Now()
	p := &Principal{Status: StatusActive}
	if err := p.SetStatus(StatusDisabled, now); err != nil {
		t.Fatalf("SetStatus(disable): %v", err)
	}
	if p.Status != StatusDisabled {
		t.Fatalf("want disabled, got %s", p.Status)
	}
	if err := p.SetStatus(StatusActive, now); err != nil {
		t.Fatalf("SetStatus(active): %v", err)
	}
	if p.Status != StatusActive {
		t.Fatalf("want active, got %s", p.Status)
	}
	if err := p.SetStatus(StatusPending, now); err == nil {
		t.Fatal("SetStatus(pending) must be rejected")
	}
}

func TestHasRole(t *testing.T) {
	p := &Principal{Roles: []string{RoleMember}}
	if !p.HasRole(RoleMember) {
		t.Fatal("member role must be found")
	}
	if p.HasRole(RoleAdmin) {
		t.Fatal("admin role must not be found")
	}
}

func TestSentinelsDistinct(t *testing.T) {
	if ErrNotFound == ErrUsernameTaken || ErrNotFound == ErrConflict || ErrUsernameTaken == ErrConflict {
		t.Fatal("persistence sentinels must be distinct values")
	}
}
