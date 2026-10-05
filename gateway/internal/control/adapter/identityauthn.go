// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"time"

	"github.com/kikakkz/looming/gateway/internal/control/app"
	"github.com/kikakkz/looming/gateway/internal/front/port"
)

// errAuthFailure is the single northbound authn failure: revoked,
// unknown, or an origin outage all look identical to the caller (fail
// closed; AD-32's northbound sameness rule — the distinctions live in
// the logs, never in the response).
var errAuthFailure = errors.New("gateway: authentication failed")

// originValidator is the validate seam, consumer-side (the
// IdentityClient is wired at composition).
type originValidator interface {
	Validate(ctx context.Context, rawKey string) (principalID, status string, err error)
}

// positiveTTLDefault bounds how long a synced or origin-confirmed entry
// authorizes without revalidation. It composes with the feed watch for
// the revocation bound: worst-case 401 latency ≈ watch latency + TTL.
const positiveTTLDefault = 30 * time.Second

// IdentityAuthenticator implements front-port Authenticator over the
// control plane's KeyCache: hash the key locally, authorize on a fresh
// active entry, else fall back to the identity validate endpoint.
// Positive results are confirmed into the cache for the TTL; negatives
// are cached nowhere (an absent entry revalidates at the origin every
// request — a wrong key costs one origin round-trip, and a revoked key
// never survives on stale cache). An origin failure, a 404, or a
// revoked status all fail closed into the same auth error.
type IdentityAuthenticator struct {
	cache  *app.KeyCache
	origin originValidator
	ttl    time.Duration
	clock  func() time.Time
	log    *slog.Logger
}

// NewIdentityAuthenticator wires the authenticator. ttl <= 0 uses the
// 30s default; clock is injected (AD-25); log nil falls back to the
// default logger.
func NewIdentityAuthenticator(cache *app.KeyCache, origin originValidator, ttl time.Duration, clock func() time.Time, log *slog.Logger) *IdentityAuthenticator {
	if ttl <= 0 {
		ttl = positiveTTLDefault
	}
	if log == nil {
		log = slog.Default()
	}
	return &IdentityAuthenticator{cache: cache, origin: origin, ttl: ttl, clock: clock, log: log}
}

var _ port.Authenticator = (*IdentityAuthenticator)(nil)

// Authenticate resolves the LoomingKey to its principal subject.
func (a *IdentityAuthenticator) Authenticate(ctx context.Context, loomKey string) (string, error) {
	hash := sha256.Sum256([]byte(loomKey))
	snap := a.cache.Get()
	if entry, ok := snap.V[hash]; ok && entry.Status == "active" {
		if age := a.clock().Sub(entry.SyncedAt); age < a.ttl {
			return entry.PrincipalID, nil
		}
		// A stale entry is not evidence either way: revalidate at the
		// origin below. The syncer may already be deleting it; the
		// confirm/delete race resolves in the deleter's favor (fail
		// closed), and the next request self-heals.
	}
	principalID, status, err := a.origin.Validate(ctx, loomKey)
	if err != nil {
		// 404 (unknown/revoked) and origin failures share one failure
		// shape northbound; the log line is the only distinction.
		a.log.InfoContext(ctx, "key validation failed closed",
			"reason", classifyValidateError(err), "err", err)
		return "", errAuthFailure
	}
	if status != "active" {
		a.log.InfoContext(ctx, "key validation returned non-active status", "status", status)
		return "", errAuthFailure
	}
	a.cache.Confirm(hash, principalID)
	return principalID, nil
}

// classifyValidateError keeps the log distinction (observability)
// without letting it leak into the response (sameness).
func classifyValidateError(err error) string {
	if errors.Is(err, ErrKeyUnknown) {
		return "unknown_or_revoked"
	}
	return "origin_failure"
}
