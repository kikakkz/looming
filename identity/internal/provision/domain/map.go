// SPDX-License-Identifier: Apache-2.0

// Package domain holds the IdentityMap aggregate (identity-l1 §2/§4):
// the per-LoomingKey bookkeeping of engine credentials. The map is
// reference-only — the aggregate never carries a plaintext credential;
// the sealed blob is the adapter's dual-track storage, the LoomingKey
// key_enc precedent (AD-35: plaintext credentials never enter this
// context). Pure model — stdlib only (AD-23/AD-24).
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Status is the map entry lifecycle. Phase 1 has a single live state:
// revocation DELETES the row (identity-l1 §3 journey 2), so the column
// exists for evolution, not for a state machine.
type Status string

// StatusActive is the only constructible status.
const StatusActive Status = "active"

// Domain and provisioning error family. Persistence sentinels mirror
// the other aggregates; the provisioning family classifies engine-side
// failures at the orchestration boundary so callers can map one 502
// shape without importing an adapter.
var (
	// ErrInvalidMap: a map entry is missing a required field.
	ErrInvalidMap = errors.New("identity: identity map entry incomplete")
	// ErrNotFound: no such (key, engine) map entry.
	ErrNotFound = errors.New("identity: identity map entry not found")
	// ErrConflict: a second entry for the same (key, engine).
	ErrConflict = errors.New("identity: identity map entry conflict")
	// ErrProvisionFailed classifies every engine-provisioning failure.
	ErrProvisionFailed = errors.New("identity: engine provisioning failed")
	// ErrUnsupportedUnit refines ErrProvisionFailed: the quota unit has
	// no engine mapping (phase 1: tokens against a LiteLLM engine).
	ErrUnsupportedUnit = fmt.Errorf("%w: engine cannot express quota unit", ErrProvisionFailed)
)

// IdentityMap maps one LoomingKey to one engine credential REFERENCE.
// Per-key granularity is the domain invariant (identity-l1 §3): the key
// is the unit of revocation, two keys never share an engine credential.
// CredentialEnc is the sealed credential value — the adapter's reveal
// track; it never renders northbound.
type IdentityMap struct {
	KeyID         string
	Engine        string
	CredentialRef string
	CredentialEnc []byte
	Status        Status
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewIdentityMap validates and builds an active entry. sealed must be
// the sealer-produced blob of the credential value; the plaintext value
// never enters the aggregate.
func NewIdentityMap(keyID, engine, credentialRef string, sealed []byte, now time.Time) (*IdentityMap, error) {
	if keyID == "" || engine == "" || credentialRef == "" || len(sealed) == 0 {
		return nil, ErrInvalidMap
	}
	return &IdentityMap{
		KeyID:         keyID,
		Engine:        engine,
		CredentialRef: credentialRef,
		CredentialEnc: sealed,
		Status:        StatusActive,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}
