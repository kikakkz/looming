// SPDX-License-Identifier: Apache-2.0

// Package port defines the host capability's persistence seam.
// Implementations are driven adapters (postgres in this component).
package port

import (
	"context"

	"github.com/kikakkz/looming/topology/internal/host/domain"
)

// Registry persists hosts. Error contract: domain.ErrNotFound for
// missing rows; domain.ErrAddressTaken when an address-uniqueness
// guard rejects the write (Register's upsert onto another host's
// address, Update's move onto a taken address).
type Registry interface {
	// Register upserts by address: a new address inserts; an address
	// already held by the same host ID refreshes that host's labels and
	// credential (idempotent re-registration); an address held by a
	// different ID fails with domain.ErrAddressTaken. The returned host
	// is the canonical stored row.
	Register(ctx context.Context, h *domain.Host) (*domain.Host, error)
	// ByID returns the host or domain.ErrNotFound.
	ByID(ctx context.Context, id string) (*domain.Host, error)
	// ByAddress returns the host or domain.ErrNotFound.
	ByAddress(ctx context.Context, address string) (*domain.Host, error)
	// Update persists an address/label change for the host identified
	// by h.ID (re-join refreshes both). An unknown ID fails with
	// domain.ErrNotFound; moving onto another host's address fails with
	// domain.ErrAddressTaken. The credential slot is not touched here —
	// T2's join mints it.
	Update(ctx context.Context, h *domain.Host) (*domain.Host, error)
}
