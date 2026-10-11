// SPDX-License-Identifier: Apache-2.0

package advisor

import _ "embed"

// defaultProfilesYAML embeds the shipped profile set so the CLI can
// consume it without a runtime file dependency (the file stays the
// repository's knowledge home; the Registry swap in slice 1.3 changes
// retrieval, not the schema — AD-38 decision 3).
//
//go:embed profiles/components.yaml
var defaultProfilesYAML []byte

// DefaultProfilesYAML returns the shipped profile-set document
// (profiles/components.yaml). Callers hash or Parse it; the bytes are
// the same review-gated artifact either way.
func DefaultProfilesYAML() []byte {
	return defaultProfilesYAML
}
