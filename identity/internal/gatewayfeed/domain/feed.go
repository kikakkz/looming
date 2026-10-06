// SPDX-License-Identifier: Apache-2.0

// Package domain holds the gateway-feed read model: the snapshot shapes
// the data plane's projection caches consume (identity-l1 §5 read
// side). Pure DTOs — the feed is a read model, not an aggregate.
package domain

import "errors"

// ErrNotFound marks a validate lookup that matched no active key.
var ErrNotFound = errors.New("identity: gateway feed key not found")

// Key status vocabulary carried on the wire. Active keys validate;
// revoked keys are listed so syncers can delete them (deterministic
// diff against the last applied snapshot — delete vs missing is never
// ambiguous).
const (
	KeyActive  = "active"
	KeyRevoked = "revoked"
)

// Principal status vocabulary (mirrors the principal aggregate's
// states; the feed reports them so consumers can fail closed on
// disabled owners without a second lookup).
const (
	PrincipalPending  = "pending"
	PrincipalActive   = "active"
	PrincipalDisabled = "disabled"
)

// Key is one row of the feed's key projection.
type Key struct {
	Hash        []byte // SHA-256 of the raw LoomingKey
	PrincipalID string
	Status      string // KeyActive | KeyRevoked
	// EngineCredential is the provisioned engine credential value for
	// this key (identity slice C contract). Empty when the key was never
	// provisioned or the credential was revoked — consumers fall back
	// per their own design. Plaintext in memory only: at rest the value
	// is sealed in identity_map, and this field is only ever filled
	// behind the service-token guard.
	EngineCredential string
}

// Principal is one row of the feed's principal projection.
type Principal struct {
	ID     string
	Status string // Principal*
}

// Snapshot is a full feed response: the complete current projection,
// never a delta. Rev is the authority's in-process monotonic revision;
// on restart Rev resets, and a response with Rev < the consumer's
// last-seen Rev is the documented full-resync signal.
type Snapshot struct {
	Rev        uint64
	Keys       []Key
	Principals []Principal
}
