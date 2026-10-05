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

// NewHost validates and builds a host at registration time. The
// role-label slice is copied.
func NewHost(id, address string, roleLabels []string, now time.Time) (*Host, error) {
	if id == "" {
		return nil, ErrInvalidHostID
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
