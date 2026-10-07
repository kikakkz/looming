// SPDX-License-Identifier: Apache-2.0

// OIDC username derivation (slice E): the configured claim's value
// becomes the principal username under the aggregate's shape rules —
// lowercased, illegal characters folded to separators, truncated to
// the username bound. An empty or all-illegal claim falls back to the
// OIDC subject (always present, always stable), so a verified
// identity can never strand without a derivable username.
package domain

import (
	"regexp"
	"strings"
)

var usernameIllegal = regexp.MustCompile(`[^a-z0-9._-]+`)

// UsernameMaxLen mirrors the principal aggregate's bound without an
// import cycle (authn-domain is below principal-domain in the matrix).
const UsernameMaxLen = 64

// DeriveUsername maps a claim value (usually an email address) and
// the OIDC subject to a valid principal username.
func DeriveUsername(claimValue, subject string) string {
	base := claimValue
	if at := strings.Index(base, "@"); at > 0 {
		base = base[:at]
	}
	base = foldUsername(base)
	if base == "" {
		// The claim was empty or carried no usable characters; the
		// subject is hex-ish and URL-safe by the OIDC spec.
		base = foldUsername(subject)
		if base == "" {
			base = "oidc-user"
		}
	}
	if len(base) > UsernameMaxLen {
		base = base[:UsernameMaxLen]
		base = strings.TrimRight(base, "._-")
	}
	return base
}

// foldUsername lowercases, folds illegal characters to separators,
// and trims edge separators — one rule for every fallback level.
func foldUsername(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = usernameIllegal.ReplaceAllString(s, "_")
	return strings.Trim(s, "._-")
}
