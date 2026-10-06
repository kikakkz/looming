// SPDX-License-Identifier: Apache-2.0
// Package port defines the interfaces the front layer orchestrates.
// These are consumer-side ports (hexagonal): implementations are
// adapters over the identity context, its cache projection, or the
// engine slot.
package port

import "context"

// Identity is what a valid LoomingKey resolves to: the caller's subject
// plus the engine credential provisioned for this key (empty when the
// key was never provisioned — the engine call then falls back to its
// configured default credential). The LoomingKey itself never leaves
// the authn step (gateway-l1 §6: engine credential southbound only).
type Identity struct {
	Subject          string
	EngineCredential string
}

// Authenticator validates the northbound Looming key and resolves the
// caller's identity. The authority is the identity context;
// implementations here are adapters over its API or its cache
// projection.
type Authenticator interface {
	// Authenticate returns the identity for a valid key. Any error means
	// the request is unauthenticated — fail closed.
	Authenticate(ctx context.Context, loomKey string) (Identity, error)
}

// SubjectAllowlist resolves a subject's model allowlist (identity /
// registry policy projection, cached in the control plane).
type SubjectAllowlist interface {
	Models(ctx context.Context, subject string) ([]string, error)
}
