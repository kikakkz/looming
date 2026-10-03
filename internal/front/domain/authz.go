// SPDX-License-Identifier: Apache-2.0
// Package dp is the front layer (gateway-l1 §5): authn fan-in,
// model-permission enforcement, interception chain, credential
// injection, event emission. Stateless; it owns no routing and no
// quota execution (AD-32).
package domain

// Decision is the outcome of one policy evaluation. The zero value is
// Deny: every check in the front layer fails closed.
type Decision bool

const (
	Deny  Decision = false
	Allow Decision = true
)

// ModelAllowed answers the model-permission question (AD-32 #3):
// may this subject use this model id? Pure function — fuzz target.
//
// allowlist is the subject's cached policy set (identity/registry
// projection, cp cache). The port contract does not guarantee order,
// so the scan is linear; an empty allowlist denies everything.
func ModelAllowed(allowlist []string, model string) Decision {
	if model == "" {
		return Deny
	}
	for _, m := range allowlist {
		if m == model {
			return Allow
		}
	}
	return Deny
}
