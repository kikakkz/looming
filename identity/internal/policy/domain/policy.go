// SPDX-License-Identifier: Apache-2.0

// Package domain holds the registration policy model: a singleton
// aggregate selecting how principals come in (identity-l1 §2).
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Mode selects the registration rule. admin-only forbids
// self-registration entirely; invite allows it with an admin voucher;
// self-register-with-approval lets anyone register into the pending
// state, awaiting approval (identity-l1 §3).
type Mode string

const (
	ModeAdminOnly             Mode = "admin-only"
	ModeInvite                Mode = "invite"
	ModeSelfRegisterWithApproval Mode = "self-register-with-approval"
)

// SingletonID is the fixed primary key: exactly one active policy holds
// structurally — the table holds one row by PK (identity-l1 §4).
const SingletonID = "singleton"

var (
	// ErrInvalidMode marks a mode outside the declared vocabulary.
	ErrInvalidMode = errors.New("identity: invalid registration policy mode")
	// ErrNotFound marks an unset policy row. Readers fail closed into
	// the admin-only default rather than trusting an empty table.
	ErrNotFound = errors.New("identity: registration policy not found")
)

// Policy is the registration policy singleton.
type Policy struct {
	ID        string // always SingletonID
	Mode      Mode
	UpdatedBy string
	UpdatedAt time.Time
}

// Valid reports whether m is a declared mode.
func (m Mode) Valid() bool {
	switch m {
	case ModeAdminOnly, ModeInvite, ModeSelfRegisterWithApproval:
		return true
	}
	return false
}

// New builds the singleton in the given mode, validating the mode and
// recording the actor for the audit column (identity-l1 §4).
func New(mode Mode, actor string, now time.Time) (*Policy, error) {
	if !mode.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrInvalidMode, mode)
	}
	return &Policy{
		ID:        SingletonID,
		Mode:      mode,
		UpdatedBy: actor,
		UpdatedAt: now,
	}, nil
}
