// SPDX-License-Identifier: Apache-2.0

// Package domain holds the Quota aggregate (identity-l1 §2/§4): the
// per-principal resource policy. Window semantics live here; engine
// budgets are the projection an EngineProvisioner writes (AD-32). Pure
// model — stdlib only (AD-23/AD-24).
package domain

import (
	"errors"
	"time"
)

// Unit is the quota denomination (identity-l1 §2). usd is the phase-1
// engine budget denomination; tokens is a valid value whose engine
// mapping is deferred — adapters reject it with a typed error.
type Unit string

const (
	// UnitTokens counts engine tokens.
	UnitTokens Unit = "tokens"
	// UnitUSD counts spend in integer cents (1/100 USD); the LiteLLM
	// adapter maps it to the proxy's float-dollar max_budget.
	UnitUSD Unit = "usd"
)

// allowedWindows is the WindowDays vocabulary (identity-l1 §4).
var allowedWindows = map[int]bool{1: true, 7: true, 30: true, 90: true}

// Domain errors. Validation errors stay safe to expose northbound; the
// repository carries ErrNoQuota for the unset state.
var (
	// ErrInvalidAmount: only non-negative amounts are quotable.
	ErrInvalidAmount = errors.New("identity: quota amount must be >= 0")
	// ErrInvalidUnit: the unit must be a Unit vocabulary value.
	ErrInvalidUnit = errors.New("identity: quota unit must be tokens or usd")
	// ErrInvalidWindow: the window must be a vocabulary value.
	ErrInvalidWindow = errors.New("identity: quota window_days must be one of 1, 7, 30, 90")
	// ErrNoQuota marks a principal with no quota row — the unlimited
	// default (identity-l1 §4).
	ErrNoQuota = errors.New("identity: no quota set")
)

// Quota is the per-principal resource policy aggregate. One row per
// principal; the row's absence is itself the default policy (unlimited),
// so deletion is not part of the phase-1 surface.
type Quota struct {
	PrincipalID string
	Amount      int64
	Unit        Unit
	WindowDays  int
	UpdatedBy   string
	UpdatedAt   time.Time
}

// NewQuota validates and builds a Quota. Amount 0 is valid and means
// blocked: the projection carries a zero budget, so engine calls fail
// their budget check until an admin raises the amount. updatedBy names
// the acting admin for the audit column.
func NewQuota(principalID string, amount int64, unit Unit, windowDays int, updatedBy string, now time.Time) (*Quota, error) {
	if amount < 0 {
		return nil, ErrInvalidAmount
	}
	if unit != UnitTokens && unit != UnitUSD {
		return nil, ErrInvalidUnit
	}
	if !allowedWindows[windowDays] {
		return nil, ErrInvalidWindow
	}
	return &Quota{
		PrincipalID: principalID,
		Amount:      amount,
		Unit:        unit,
		WindowDays:  windowDays,
		UpdatedBy:   updatedBy,
		UpdatedAt:   now,
	}, nil
}
