// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"errors"
	"testing"
	"time"
)

var mapNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func TestNewIdentityMapValidation(t *testing.T) {
	cases := []struct {
		name    string
		keyID   string
		engine  string
		ref     string
		sealed  []byte
		wantErr error
	}{
		{"complete", "k-1", "litellm", "ref-1", []byte("sealed"), nil},
		{"empty key id", "", "litellm", "ref-1", []byte("sealed"), ErrInvalidMap},
		{"empty engine", "k-1", "", "ref-1", []byte("sealed"), ErrInvalidMap},
		{"empty credential ref", "k-1", "litellm", "", []byte("sealed"), ErrInvalidMap},
		{"empty sealed blob", "k-1", "litellm", "ref-1", nil, ErrInvalidMap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewIdentityMap(tc.keyID, tc.engine, tc.ref, tc.sealed, mapNow)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
			if tc.wantErr != nil {
				return
			}
			if m.KeyID != tc.keyID || m.Engine != tc.engine || m.CredentialRef != tc.ref ||
				m.Status != StatusActive || !m.CreatedAt.Equal(mapNow) || !m.UpdatedAt.Equal(mapNow) {
				t.Fatalf("map entry mismatch: %+v", m)
			}
		})
	}
}

func TestSentinelFamily(t *testing.T) {
	// The provisioner error family classifies every engine-side failure
	// at the orchestration boundary; ErrUnsupportedUnit refines it when a
	// quota unit has no engine mapping.
	if !errors.Is(ErrUnsupportedUnit, ErrProvisionFailed) {
		t.Fatal("ErrUnsupportedUnit must refine ErrProvisionFailed")
	}
	for _, err := range []error{ErrProvisionFailed, ErrUnsupportedUnit, ErrNotFound, ErrConflict} {
		if err == nil || err.Error() == "" {
			t.Fatalf("sentinel must be non-empty")
		}
	}
}
