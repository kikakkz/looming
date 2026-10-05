// SPDX-License-Identifier: Apache-2.0

// Package port defines the guide capability's persistence seam.
// Implementations are driven adapters (postgres in this component).
package port

import (
	"context"

	"github.com/kikakkz/looming/topology/internal/guide/domain"
)

// Store persists the Guide aggregate. Error contract:
// domain.ErrNoGuide on reads when nothing was rendered yet.
type Store interface {
	// Current returns the persisted guide, or domain.ErrNoGuide.
	Current(ctx context.Context) (domain.Guide, error)
	// Save upserts the singleton guide row, replacing any previous
	// snapshot — re-rendering is a converge, not a history.
	Save(ctx context.Context, g domain.Guide) error
}
