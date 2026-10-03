// SPDX-License-Identifier: Apache-2.0
package domain

import (
	"strings"
	"testing"
)

func TestModelAllowed(t *testing.T) {
	list := []string{"deepseek-chat", "gpt-5", "internal-qwen"}
	cases := []struct {
		name      string
		allowlist []string
		model     string
		want      Decision
	}{
		{"listed", list, "gpt-5", Allow},
		{"unsorted list still matches", []string{"zeta", "gpt-5", "alpha"}, "gpt-5", Allow},
		{"unlisted", list, "claude-3", Deny},
		{"empty list denies all", nil, "gpt-5", Deny},
		{"empty model denied", list, "", Deny},
		{"nil list vs empty model", nil, "", Deny},
		{"prefix is not a match", list, "gpt", Deny},
		{"suffix is not a match", list, "gpt-5-turbo", Deny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelAllowed(tc.allowlist, tc.model); got != tc.want {
				t.Fatalf("ModelAllowed(%v, %q) = %v, want %v", tc.allowlist, tc.model, got, tc.want)
			}
		})
	}
}

func FuzzModelAllowed(f *testing.F) {
	// f.Add takes scalars only: the seed allowlist travels as a
	// newline-joined string.
	f.Add("a\nb\nc", "b")
	f.Add("", "anything")
	f.Add("x", "")
	f.Fuzz(func(t *testing.T, seeded string, model string) {
		var allowlist []string
		for _, s := range strings.Split(seeded, "\n") {
			if s != "" {
				allowlist = append(allowlist, s)
			}
		}
		got := ModelAllowed(allowlist, model)
		if model == "" && got != Deny {
			t.Fatalf("empty model must deny, got %v", got)
		}
		if len(allowlist) == 0 && got != Deny {
			t.Fatalf("empty allowlist must deny, got %v", got)
		}
	})
}
