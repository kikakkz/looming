// SPDX-License-Identifier: Apache-2.0

// Package port defines the topology capability's persistence seam.
// Implementations are driven adapters (postgres in this component).
package port

import (
	"context"

	"github.com/kikakkz/looming/topology/internal/topology/domain"
)

// Store persists the Topology aggregate. Error contract:
// domain.ErrNoTopology on reads against an uninitialized deployment;
// domain.ErrConflict when the optimistic-revision guard or a placement
// uniqueness constraint rejects the write.
type Store interface {
	// Save persists a topology transition: the singleton row is
	// upserted guarded by t.Revision (the stored revision must be
	// t.Revision-1, so a first declare carries Revision 1) and the
	// placement set is replaced wholesale in the same transaction. A
	// lost revision race fails with domain.ErrConflict; a (component,
	// host) uniqueness violation — a domain-validation bypass below
	// the app — fails with domain.ErrDuplicatePlacement.
	Save(ctx context.Context, t domain.Topology) error
	// Current returns the persisted topology with its placement set, or
	// domain.ErrNoTopology when nothing has been declared yet.
	Current(ctx context.Context) (domain.Topology, error)
}
