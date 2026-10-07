// SPDX-License-Identifier: Apache-2.0

package domain

import "testing"

func TestDeriveUsername(t *testing.T) {
	cases := []struct {
		name  string
		claim string
		sub   string
		want  string
	}{
		{"email local-part lowercased", "Jane.Doe@Example.com", "s1", "jane.doe"},
		{"plus-addressing folds away", "jane+dev@example.com", "s1", "jane_dev"},
		{"illegal characters fold to separator", "jane doe@example.com", "s1", "jane_doe"},
		{"empty claim falls back to subject-folded", "", "auth0|abc123", "auth0_abc123"},
		{"claim without usable characters falls back", "!!!", "sub-9", "sub-9"},
		{"no at-sign passes through cleaned", "jane_doe", "s1", "jane_doe"},
		{"leading separators trimmed", "..jane..@x.com", "s1", "jane"},
		{"over-long claim truncated", string(makeLongLocal()), "s1", string(makeLongLocal()[:64])},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveUsername(tc.claim, tc.sub); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func makeLongLocal() []byte {
	b := make([]byte, 80)
	for i := range b {
		b[i] = 'a'
	}
	return b
}
