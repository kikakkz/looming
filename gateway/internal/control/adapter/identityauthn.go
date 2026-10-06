// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"time"

	"github.com/kikakkz/looming/gateway/internal/control/app"
	frontport "github.com/kikakkz/looming/gateway/internal/front/port"
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
// control plane's KeyCache: hash the key locally, authorize on a
// usable entry, else fall back to the identity validate endpoint.
//
// Two entry kinds live in the cache, with different freshness rules.
// Feed-synced entries (Confirmed=false) authorize on presence alone
// while the syncer is healthy: the feed delete is their revocation
// path (revocation bound = watch latency), and a stalled syncer turns
// them back into misses so a stale projection never keeps authorizing.
// Origin-confirmed entries (Confirmed=true) authorize for the positive
// TTL, then must revalidate — the origin's word ages. Negatives are
// cached nowhere (a wrong key costs one origin round-trip per request,
// and a revoked key never survives on stale cache). Any origin
// failure, 404, or non-active status fails closed into the same auth
// error (AD-32 northbound sameness; distinctions live in the logs).
type IdentityAuthenticator struct {
	cache   *app.KeyCache
	origin  originValidator
	ttl     time.Duration
	clock   func() time.Time
	log     *slog.Logger
	healthy func() bool // syncer health probe, wired to Syncer.Healthy
}

// NewIdentityAuthenticator wires the authenticator. ttl <= 0 uses the
// 30s default; clock is injected (AD-25); log nil falls back to the
// default logger; healthy nil means "syncer unhealthy" (feed entries
// never authorize without a probe).
func NewIdentityAuthenticator(cache *app.KeyCache, origin originValidator, ttl time.Duration, clock func() time.Time, log *slog.Logger, healthy func() bool) *IdentityAuthenticator {
	if ttl <= 0 {
		ttl = positiveTTLDefault
	}
	if log == nil {
		log = slog.Default()
	}
	if healthy == nil {
		healthy = func() bool { return false }
	}
	return &IdentityAuthenticator{cache: cache, origin: origin, ttl: ttl, clock: clock, log: log, healthy: healthy}
}

var _ frontport.Authenticator = (*IdentityAuthenticator)(nil)

// Authenticate resolves the LoomingKey to its caller identity: the
// subject plus the provisioned engine credential. The cache-hit path
// returns the projected credential; the origin fallback authorizes
// with an EMPTY credential — identity's validate answers (principal,
// status) only, so the feed fills the value on its next sync and the
// engine falls back to its configured default until then.
func (a *IdentityAuthenticator) Authenticate(ctx context.Context, loomKey string) (frontport.Identity, error) {
	hash := sha256.Sum256([]byte(loomKey))
	snap := a.cache.Get()
	if entry, ok := snap.V[hash]; ok && entry.Status == "active" {
		fresh := a.clock().Sub(entry.SyncedAt) < a.ttl
		if fresh || (!entry.Confirmed && a.healthy()) {
			return frontport.Identity{Subject: entry.PrincipalID, EngineCredential: entry.EngineCredential}, nil
		}
		// A stale confirmed entry is only as good as its last origin
		// check; a feed entry with a stalled syncer is a snapshot of
		// unknown age. Both revalidate below. The syncer may already
		// be deleting the key; the confirm/delete race resolves in
		// the deleter's favor (fail closed), and the next request
		// self-heals.
	}
	principalID, status, err := a.origin.Validate(ctx, loomKey)
	if err != nil {
		// 404 (unknown/revoked) and origin failures share one failure
		// shape northbound; the log line is the only distinction.
		a.log.InfoContext(ctx, "key validation failed closed",
			"reason", classifyValidateError(err), "err", err)
		return frontport.Identity{}, errAuthFailure
	}
	if status != "active" {
		a.log.InfoContext(ctx, "key validation returned non-active status", "status", status)
		return frontport.Identity{}, errAuthFailure
	}
	a.cache.Confirm(hash, principalID)
	return frontport.Identity{Subject: principalID}, nil
}

// classifyValidateError keeps the log distinction (observability)
// without letting it leak into the response (sameness).
func classifyValidateError(err error) string {
	if errors.Is(err, ErrKeyUnknown) {
		return "unknown_or_revoked"
	}
	return "origin_failure"
}
