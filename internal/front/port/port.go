// Package port defines the interfaces the front layer orchestrates.
// These are consumer-side ports (hexagonal): implementations are
// adapters over the identity context, its cache projection, or the
// engine slot.
package port

import "context"

// Authenticator validates the northbound Looming key and resolves the
// subject. The authority is the identity context; implementations here
// are adapters over its API or its cache projection.
type Authenticator interface {
	// Authenticate returns the subject for a valid key. Any error means
	// the request is unauthenticated — fail closed.
	Authenticate(ctx context.Context, loomKey string) (subject string, err error)
}

// SubjectAllowlist resolves a subject's model allowlist (identity /
// registry policy projection, cached in the control plane).
type SubjectAllowlist interface {
	Models(ctx context.Context, subject string) ([]string, error)
}
