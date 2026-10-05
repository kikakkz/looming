// SPDX-License-Identifier: Apache-2.0

package port

import "context"

// Guide is the topology-rendered onboarding data the public page
// serves (topology-l1 §5 Guide row). The gateway owns the page; the
// topology component owns this data — the wire shape is the topology
// snapshot verbatim, defined gateway-side (AD-34: no shared platform
// lib).
type Guide struct {
	ClusterName    string
	AccessPublic   bool
	CLIDownloadURL string
	IdentityURL    string
	GatewayURL     string
	Steps          []string
	RegisterHint   string
}

// GuideSource fetches the current guide from the topology service —
// the GuidePublisher port's driving adapter (topology-l1 §6). The
// implementation owns caching policy; the page handler only renders.
type GuideSource interface {
	// FetchGuide returns the current guide. Any error means the fetch
	// failed; the caller decides between last-known-good and a 503.
	FetchGuide(ctx context.Context) (Guide, error)
}
