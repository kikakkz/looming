// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"errors"
	"testing"
	"time"
)

var quotaNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func TestNewQuotaValidationMatrix(t *testing.T) {
	cases := []struct {
		name    string
		amount  int64
		unit    Unit
		window  int
		wantErr error
	}{
		{"usd daily", 500, UnitUSD, 1, nil},
		{"usd weekly", 1000, UnitUSD, 7, nil},
		{"tokens monthly", 250000, UnitTokens, 30, nil},
		{"usd quarterly", 99999, UnitUSD, 90, nil},
		{"zero is valid (blocked)", 0, UnitUSD, 1, nil},
		{"negative amount", -1, UnitUSD, 7, ErrInvalidAmount},
		{"unknown unit", 5, Unit("requests"), 7, ErrInvalidUnit},
		{"empty unit", 5, Unit(""), 7, ErrInvalidUnit},
		{"window 0", 5, UnitUSD, 0, ErrInvalidWindow},
		{"window 3", 5, UnitUSD, 3, ErrInvalidWindow},
		{"window 365", 5, UnitUSD, 365, ErrInvalidWindow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := NewQuota("p-1", tc.amount, tc.unit, tc.window, "admin-1", quotaNow)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
			if tc.wantErr != nil {
				return
			}
			if q.PrincipalID != "p-1" || q.Amount != tc.amount || q.Unit != tc.unit ||
				q.WindowDays != tc.window || q.UpdatedBy != "admin-1" || !q.UpdatedAt.Equal(quotaNow) {
				t.Fatalf("quota mismatch: %+v", q)
			}
		})
	}
}

func TestZeroAmountMeansBlocked(t *testing.T) {
	// Amount 0 is a deliberate administrative state, not an absence of
	// policy: the engine projection carries a zero budget, so every
	// engine call fails its budget check until an admin raises it. The
	// "no policy" state is the ABSENT row (ErrNoQuota at the repository).
	q, err := NewQuota("p-1", 0, UnitUSD, 30, "admin-1", quotaNow)
	if err != nil {
		t.Fatalf("zero amount must construct: %v", err)
	}
	if q.Amount != 0 {
		t.Fatalf("zero amount must round-trip, got %d", q.Amount)
	}
	if errors.Is(err, ErrInvalidAmount) {
		t.Fatal("zero must not trip the amount floor")
	}
}

func TestUnitVocabulary(t *testing.T) {
	if UnitTokens != "tokens" || UnitUSD != "usd" {
		t.Fatalf("unit wire values changed: %q %q", UnitTokens, UnitUSD)
	}
}

func TestNoQuotaSentinel(t *testing.T) {
	if ErrNoQuota == nil || ErrNoQuota.Error() == "" {
		t.Fatal("ErrNoQuota must exist for the repository's unset shape")
	}
}
