// SPDX-License-Identifier: Apache-2.0
package port_test

import (
	"context"
	"testing"
	"time"

	"github.com/kikakkz/looming/identity/internal/authn/port"
)

func TestTokenInfoContextRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	want := port.TokenInfo{
		PrincipalID: "p-1",
		Roles:       []string{"admin"},
		ExpiresAt:   now,
	}
	ctx := port.WithTokenInfo(context.Background(), want)
	got, ok := port.TokenInfoFrom(ctx)
	if !ok {
		t.Fatal("TokenInfo must be present after WithTokenInfo")
	}
	if got.PrincipalID != want.PrincipalID || got.ExpiresAt != want.ExpiresAt || len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestTokenInfoFromMissing(t *testing.T) {
	if _, ok := port.TokenInfoFrom(context.Background()); ok {
		t.Fatal("TokenInfo must be absent from a fresh context")
	}
}
