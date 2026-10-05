// SPDX-License-Identifier: Apache-2.0

// Package app orchestrates the guide capability's use cases
// (topology-l1 §6 GuidePublisher): rendering the guide from the current
// Topology snapshot and serving the persisted copy. Rendering is
// idempotent — the persisted copy is rewritten only when the Topology
// revision moved.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kikakkz/looming/topology/internal/guide/domain"
	"github.com/kikakkz/looming/topology/internal/guide/port"
	hostport "github.com/kikakkz/looming/topology/internal/host/port"
	topologydomain "github.com/kikakkz/looming/topology/internal/topology/domain"
	topologyport "github.com/kikakkz/looming/topology/internal/topology/port"
)

// placementHTTPPort is the named placement port the guide URLs read —
// the same contract key the renderer's component contracts declare.
const placementHTTPPort = "http"

// Service orchestrates guide render/read over the guide store, the
// topology snapshot, and the host registry (placement → address →
// URL). clock is injected (AD-25: no wall-clock in the use cases).
type Service struct {
	guides   port.Store
	topology topologyport.Store
	registry hostport.Registry
	clock    func() time.Time
}

// NewService wires the service.
func NewService(guides port.Store, topology topologyport.Store, registry hostport.Registry, clock func() time.Time) *Service {
	return &Service{guides: guides, topology: topology, registry: registry, clock: clock}
}

// Facts are the two render inputs the Topology cannot supply — the
// config-level presentation facts (cluster name, CLI download URL).
type Facts struct {
	ClusterName    string
	CLIDownloadURL string
}

// EnsureRendered returns the persisted guide, rendering and persisting
// a fresh one when the persisted copy is absent or was rendered from
// an older Topology revision. The bool reports whether a render
// happened — apply logs it, and the CLI's `guide show` prints fresh
// data without waiting for the next converge.
func (s *Service) EnsureRendered(ctx context.Context, f Facts) (domain.Guide, bool, error) {
	topo, err := s.topology.Current(ctx)
	if err != nil {
		return domain.Guide{}, false, fmt.Errorf("guide: read topology: %w", err)
	}

	guide, err := s.guides.Current(ctx)
	if err == nil && guide.CurrentFor(topo.Revision) {
		return guide, false, nil
	}
	if err != nil && !errors.Is(err, domain.ErrNoGuide) {
		return domain.Guide{}, false, fmt.Errorf("guide: read persisted: %w", err)
	}

	guide = domain.Render(renderFacts(ctx, topo, f, s.registry), s.clock())
	if err := s.guides.Save(ctx, guide); err != nil {
		return domain.Guide{}, false, fmt.Errorf("guide: persist: %w", err)
	}
	return guide, true, nil
}

// Current returns the persisted guide without rendering — the
// topologyd internal endpoint's read path. domain.ErrNoGuide passes
// through: the endpoint maps it to 404 rather than inventing data.
func (s *Service) Current(ctx context.Context) (domain.Guide, error) {
	return s.guides.Current(ctx)
}

// renderFacts derives the guide input from the Topology snapshot:
// access mode plus identityd/gateway-front placement URLs (host
// address + http port, the phase-1 http://<address>:<port> shape).
// Every gap degrades to an empty URL — the guide must render for any
// declared topology, not fail for want of onboarding trivia (the
// endpointHint precedent in the join capability).
func renderFacts(ctx context.Context, topo topologydomain.Topology, f Facts, registry hostport.Registry) domain.Facts {
	return domain.Facts{
		Revision:       topo.Revision,
		AccessPublic:   topo.Access.Mode == topologydomain.ModePublic,
		ClusterName:    f.ClusterName,
		CLIDownloadURL: f.CLIDownloadURL,
		IdentityURL:    placementURL(ctx, topo, registry, topologydomain.ComponentIdentityd),
		GatewayURL:     placementURL(ctx, topo, registry, topologydomain.ComponentGatewayFront),
	}
}

// placementURL resolves one component's base URL from its placement.
// No placement, no http port, or no host row all degrade to "".
func placementURL(ctx context.Context, topo topologydomain.Topology, registry hostport.Registry, component string) string {
	for _, p := range topo.Placements {
		if p.Component != component {
			continue
		}
		port, ok := p.Ports[placementHTTPPort]
		if !ok {
			return ""
		}
		host, err := registry.ByID(ctx, p.HostID)
		if err != nil || host == nil {
			return ""
		}
		return fmt.Sprintf("http://%s:%d", host.Address, port)
	}
	return ""
}
