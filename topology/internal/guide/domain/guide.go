// SPDX-License-Identifier: Apache-2.0

// Package domain holds the Guide aggregate (topology-l1 §5): the
// rendered onboarding artifact served at the gateway's public route.
// The render input is the current Topology snapshot plus the two
// presentation facts that live only in the config (cluster name, CLI
// download URL); the snapshot carries zero credentials by invariant —
// the shape is fixed and asserted structurally in tests.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// ErrNoGuide marks reads against a deployment whose guide was never
// rendered (apply renders it after every successful converge, so this
// is the pre-first-apply window).
var ErrNoGuide = errors.New("guide: no guide rendered yet")

// SingletonID is the Guide row's fixed primary key — the singleton
// invariant holds structurally, like the Topology aggregate's.
const SingletonID = "singleton"

// Snapshot is the guide's data: the JSON topologyd serves at its
// internal endpoint and the gateway page renders. The field set is a
// wire contract with the gateway component — adding a field is a
// contract change across components (AD-34: no shared platform lib).
type Snapshot struct {
	ClusterName    string   `json:"cluster_name"`
	AccessPublic   bool     `json:"access_public"`
	CLIDownloadURL string   `json:"cli_download_url"`
	IdentityURL    string   `json:"identity_url"`
	GatewayURL     string   `json:"gateway_url"`
	Steps          []string `json:"steps"`
	RegisterHint   string   `json:"register_hint"`
}

// Guide is the aggregate: the rendered snapshot, stamped with the
// Topology revision it was rendered from and the render time.
type Guide struct {
	ID          string
	Snapshot    Snapshot
	RenderedRev int64
	RenderedAt  time.Time
}

// Facts is the render input, expressed in guide terms — the app layer
// derives it from the persisted Topology snapshot and the host
// registry (placement → address → URL). Every field is public-page
// safe by construction: no credential-shaped input exists.
type Facts struct {
	Revision       int64
	AccessPublic   bool
	ClusterName    string
	CLIDownloadURL string
	IdentityURL    string
	GatewayURL     string
}

// Render builds the guide for one topology revision. It is pure and
// total: missing URL facts degrade to generic step wording rather than
// failing — the page must render for every declared topology.
func Render(f Facts, now time.Time) Guide {
	return Guide{
		ID:          SingletonID,
		RenderedRev: f.Revision,
		RenderedAt:  now,
		Snapshot: Snapshot{
			ClusterName:    f.ClusterName,
			AccessPublic:   f.AccessPublic,
			CLIDownloadURL: f.CLIDownloadURL,
			IdentityURL:    f.IdentityURL,
			GatewayURL:     f.GatewayURL,
			Steps:          steps(f),
			RegisterHint:   registerHint(f),
		},
	}
}

// CurrentFor reports whether the guide already reflects the given
// topology revision — the idempotent re-render rule (topology-l1 §5:
// regenerated on every apply; an unchanged revision must not rewrite).
func (g Guide) CurrentFor(revision int64) bool {
	return g.RenderedRev == revision
}

// steps is the fixed journey wording (topology-l1 §3 journey 3):
// download the CLI → register (or ask an admin) → create a key →
// configure agents. Only the facts interpolate; no per-deployment
// prose is invented.
func steps(f Facts) []string {
	register := fmt.Sprintf("Register at %s.", f.IdentityURL)
	if f.IdentityURL == "" {
		register = "Register an account (your admin has the address)."
	}
	agents := fmt.Sprintf("Configure your agents to call the gateway at %s.", f.GatewayURL)
	if f.GatewayURL == "" {
		agents = "Configure your agents to call the cluster gateway."
	}
	return []string{
		fmt.Sprintf("Download the Looming CLI from %s.", f.CLIDownloadURL),
		register,
		"Create an API key for your account.",
		agents,
	}
}

func registerHint(f Facts) string {
	where := "your cluster admin"
	if f.IdentityURL != "" {
		where = fmt.Sprintf("your cluster admin (registration is at %s)", f.IdentityURL)
	}
	return fmt.Sprintf("No account yet? Ask %s for an invite token — registration needs one.", where)
}
