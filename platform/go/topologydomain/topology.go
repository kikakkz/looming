// SPDX-License-Identifier: Apache-2.0

// Package domain holds the Topology aggregate and its value objects:
// the desired-state snapshot of the deployment (topology-l1 §2/§5) —
// the singleton aggregate with its optimistic revision, the phase-1
// Access vocabulary, and the ComponentPlacement value object with its
// placement-set invariants. Pure model — stdlib only (AD-23/AD-24).
package domain

import (
	"errors"
	"time"
)

// Persistence-facing sentinels. Uniqueness and optimistic concurrency
// are aggregate invariants, not adapter details, so the sentinels live
// beside the model — the identity precedent.
var (
	// ErrNoHosts marks a declare without hosts — the hosts ≥ 1
	// invariant (topology-l1 §5) checked at the app boundary, since
	// Host is its own aggregate.
	ErrNoHosts = errors.New("topology: at least one host must be declared")
	// ErrNoTopology marks reads against an uninitialized deployment:
	// no topology has been declared yet.
	ErrNoTopology = errors.New("topology: no topology declared yet")
	// ErrConflict marks a write that lost the optimistic-revision race
	// or hit a uniqueness guard: the caller must re-read and retry.
	ErrConflict = errors.New("topology: conflicting write")
)

// SingletonID is the Topology row's fixed primary key — the singleton
// invariant holds structurally (topology-l1 §5).
const SingletonID = "singleton"

// Topology is the desired-state snapshot aggregate (topology-l1 §2):
// the access section plus the component-placement set, versioned by an
// optimistic Revision. Revision 0 means never declared; the first
// successful declare persists Revision 1.
type Topology struct {
	ID         string
	Access     Access
	Revision   int64
	Placements []ComponentPlacement
	UpdatedAt  time.Time
}

// Uninitialized returns the zero topology: the Revision-0 base the
// first declare builds on.
func Uninitialized() Topology {
	return Topology{ID: SingletonID}
}

// Next returns the topology that persisting this state transition
// produces: same identity, the new access and placement set deep-copied,
// revision bumped by one, timestamp advanced. It is pure — the receiver
// is left unchanged.
func (t Topology) Next(access Access, placements []ComponentPlacement, now time.Time) Topology {
	return Topology{
		ID:         t.ID,
		Access:     access,
		Revision:   t.Revision + 1,
		Placements: copyPlacements(placements),
		UpdatedAt:  now,
	}
}

// Matches reports whether the topology already describes exactly the
// desired access and placement set (order-insensitive). Declare uses it
// to keep converge a no-op: an identical declare must not bump the
// revision (topology-l1 §8 converge-idempotency).
func (t Topology) Matches(access Access, placements []ComponentPlacement) bool {
	return t.Access == access && samePlacements(t.Placements, placements)
}
