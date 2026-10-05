// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
)

// CredentialByteLen is the raw random material behind one host
// credential: a successful join mints 32 bytes and hands the plaintext
// to the joining host exactly once (topology-l1 §5 Host row).
const CredentialByteLen = 32

// GenerateCredential mints a host's persistent re-join credential:
// 32 random bytes, base64url raw for transport/header use, SHA-256 hash
// for storage in hosts.credential_hash. rng is injected (AD-25).
func GenerateCredential(rng io.Reader) (raw string, hash []byte, err error) {
	buf := make([]byte, CredentialByteLen)
	if _, err := io.ReadFull(rng, buf); err != nil {
		return "", nil, fmt.Errorf("topology: host credential randomness: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	return raw, sum[:], nil
}

// CredentialMatches verifies a presented re-join credential against the
// stored hash. The comparison is constant-time (CWE-208) and a host
// whose slot is empty (never joined) matches nothing — a wrong or
// absent credential is indistinguishable from an unknown host.
func (h *Host) CredentialMatches(raw string) bool {
	if len(h.CredentialHash) == 0 || raw == "" {
		return false
	}
	sum := sha256.Sum256([]byte(raw))
	return subtle.ConstantTimeCompare(h.CredentialHash, sum[:]) == 1
}
