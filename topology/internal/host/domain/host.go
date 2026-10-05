// SPDX-License-Identifier: Apache-2.0

// Package domain holds the Host aggregate: a registered machine in the
// topology (topology-l1 §5 Host row) — its unique address, role labels,
// join time, and the persistent re-join service credential slot that
// stays empty until T2's join mints it. Pure model — stdlib only
// (AD-23/AD-24).
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Persistence-facing sentinels; uniqueness is an aggregate invariant,
// not an adapter detail — the identity precedent.
var (
	// ErrInvalidHostID marks a host built without an ID.
	ErrInvalidHostID = errors.New("topology: host id required")
	// ErrInvalidAddress marks a host built without a usable address.
	// Phase 1 carries plain IPs (topology-l1 §1); DNS-shaped addresses
	// arrive with #111 and are a parse-layer concern, not enforced here.
	ErrInvalidAddress = errors.New("topology: host address required")
	// ErrNotFound marks a lookup that matched no host.
	ErrNotFound = errors.New("topology: host not found")
	// ErrAddressTaken marks a register or address move onto an address
	// another host already holds — the address-uniqueness invariant.
	ErrAddressTaken = errors.New("topology: host address already taken")
)

// Host is the registered-machine aggregate. CredentialHash is the
// host's persistent service credential for pull re-join (topology-l1
// §5): nil until T2's join flow mints it; the aggregate never contains
// a heartbeat — liveness is the supervisor's concern.
type Host struct {
	ID             string
	Address        string
	RoleLabels     []string
	CredentialHash []byte
	JoinedAt       time.Time
}

// NewHost validates and builds a host at registration time. The ID
// must be a UUID in canonical shape — the hosts table's id column is
// uuid, so a malformed ID must fail here at the model boundary, not at
// the adapter. The role-label slice is copied.
func NewHost(id, address string, roleLabels []string, now time.Time) (*Host, error) {
	if !validUUID(id) {
		return nil, fmt.Errorf("%w: %q is not a canonical UUID", ErrInvalidHostID, id)
	}
	if strings.TrimSpace(address) == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalidAddress)
	}
	return &Host{
		ID:         id,
		Address:    address,
		RoleLabels: append([]string(nil), roleLabels...),
		JoinedAt:   now,
	}, nil
}

// validUUID reports whether id has the canonical 8-4-4-4-12 hex shape
// produced by uuid.NewString. The model checks shape only; parsing is
// persistence's job on the way into the uuid column.
func validUUID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isHex(c) {
			return false
		}
	}
	return true
}

func isHex(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
