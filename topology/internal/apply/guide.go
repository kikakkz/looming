// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"context"

	"github.com/kikakkz/looming/topology/internal/config"
	guideapp "github.com/kikakkz/looming/topology/internal/guide/app"
)

// Guide-render step (topology-l1 §5 Guide row, §6 GuidePublisher):
// after a successful converge, apply renders the public onboarding
// guide from the persisted topology revision plus the config's
// presentation facts (cluster name, CLI download URL). The persisted
// copy is rewritten only when the revision moved — an unchanged apply
// is a no-op end to end. A failure warns on the result instead of
// failing the converge that already succeeded (the invite precedent).

// GuideOutcome is the guide step's operator-facing result: Rendered
// marks a fresh snapshot persisted; Err carries a failure (reported,
// never fatal).
type GuideOutcome struct {
	Rendered bool
	Err      error
}

// renderGuide runs the guide step unconditionally (every deployment
// gets a guide, declared or not — the endpoint answers from the
// persisted row either way).
func (p *Pipeline) renderGuide(ctx context.Context, stores Stores, cfg *config.Config, result *Result) {
	svc := guideapp.NewService(stores.Guides, stores.Topology, stores.Registry, p.deps.Clock)
	_, rendered, err := svc.EnsureRendered(ctx, guideapp.Facts{
		ClusterName:    cfg.ClusterName,
		CLIDownloadURL: cfg.CLIDownloadURL,
	})
	outcome := &GuideOutcome{Rendered: rendered}
	if err != nil {
		outcome.Err = err
	}
	result.Guide = outcome
}
