// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/kikakkz/looming/gateway/internal/control/domain"
)

// FeedKey is one gateway-side shape of an identity feed key row (the
// DTO is defined independently in each component; AD-34 rule 2 — no
// shared platform lib yet).
type FeedKey struct {
	Hash        [32]byte
	PrincipalID string
	Status      string // "active" | "revoked" (feed vocabulary)
	// EngineCredential is the per-key engine credential the identity
	// feed projected for this row (identity slice C contract): present
	// only for provisioned keys, empty otherwise — the gateway then
	// falls back to its configured default upstream credential.
	EngineCredential string
}

// FeedResponse is the gateway-side shape of an identity feed snapshot:
// the full projection, never a delta.
type FeedResponse struct {
	Rev        uint64
	Keys       []FeedKey
	Principals []FeedPrincipal
}

// FeedPrincipal is one principal row of the feed: status plus the
// effective permission set (union over builtin role bundles,
// deterministic order — identity slice D). The gateway intersects
// model:use:* with its own catalog; identity never evaluates it.
type FeedPrincipal struct {
	ID          string
	Status      string
	Permissions []string
}

// FeedSource is the syncer's consumer-side seam over the identity HTTP
// client. Snapshot returns immediately; Watch blocks in identity's
// long-poll shape until the revision advances or the watch timeout
// elapses.
type FeedSource interface {
	Snapshot(ctx context.Context) (FeedResponse, error)
	Watch(ctx context.Context, sinceRev uint64) (FeedResponse, error)
}

// feedActive is the only status the key projection retains.
const feedActive = "active"

// Syncer keeps the KeyCache a faithful projection of the identity
// feed: boot fetches a full snapshot, then watches long-poll for
// revisions, applying diffs (upserts for active keys, deletes for
// revoked or vanished ones). A response revision below the last
// applied one is the authority-restart signal: the projection resets
// wholesale and watching resumes from the new revision. Transport
// failures retry under the control plane's bounded backoff (no silent
// drops — component-patterns #4: failures are logged, the last good
// snapshot keeps serving).
type Syncer struct {
	source     FeedSource
	cache      *KeyCache
	allowlist  *ModelAllowlistCache
	catalog    []string
	backoffer  domain.Backoffer
	staleAfter time.Duration
	clock      func() time.Time
	log        *slog.Logger
	lastSyncAt atomic.Int64 // unix nanos of the last applied sync; 0 = never
}

// NewSyncer wires the syncer. clock is injected (AD-25); log nil falls
// back to the default logger. allowlist may be nil (the model-
// permission projection is optional — a nil cache skips it); catalog
// is the gateway's model catalog (AD-32: engine-side configuration the
// model:use:* wildcard expands against).
func NewSyncer(source FeedSource, cache *KeyCache, allowlist *ModelAllowlistCache, catalog []string, backoffer domain.Backoffer, staleAfter time.Duration, clock func() time.Time, log *slog.Logger) *Syncer {
	if log == nil {
		log = slog.Default()
	}
	return &Syncer{source: source, cache: cache, allowlist: allowlist, catalog: catalog, backoffer: backoffer, staleAfter: staleAfter, clock: clock, log: log}
}

// Run drives the full→watch→diff loop until ctx ends; it returns nil
// on clean shutdown. Every transport error is logged and retried under
// backoff — the projection serves its last good snapshot meanwhile,
// and Healthy() reports the staleness. Boot state is tracked
// separately from the revision: identity starts at revision 0 (and a
// restart can roll back to it), so "since == 0" is not a safe boot
// marker — at rev 0 the watch must still hold instead of busy-looping
// full snapshots, and the boot snapshot must bypass the cache's
// monotonic guard (Apply(rev 0) would suppress it).
func (s *Syncer) Run(ctx context.Context) error {
	var since uint64
	booted := false
	attempt := 0
	for {
		var (
			resp FeedResponse
			err  error
		)
		if booted {
			resp, err = s.source.Watch(ctx, since)
		} else {
			resp, err = s.source.Snapshot(ctx)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.log.ErrorContext(ctx, "identity feed fetch failed, retrying",
				"since_rev", since, "attempt", attempt+1, "err", err)
			delay := s.backoffer.DelayFor(attempt)
			attempt++
			if !sleepCtx(ctx, delay) {
				return nil
			}
			continue
		}
		attempt = 0
		if !booted || resp.Rev < since {
			// First snapshot, or an authority restart: the response is
			// already the full current projection — reset wholesale
			// (omissions are deletions, fail closed) and resume from
			// the new rev. The boot pass needs Reset because the cache
			// starts at rev 0 and Apply(0) would be suppressed.
			if booted {
				s.log.InfoContext(ctx, "identity feed revision rolled back, resetting projection",
					"prev_rev", since, "new_rev", resp.Rev)
			}
			s.cache.Reset(Revision(resp.Rev), activeEntries(resp, s.clock()))
			s.projectAllowlist(resp)
			booted = true
			since = resp.Rev
			s.markSynced()
			continue
		}
		s.applyDiff(resp)
		since = resp.Rev
		s.markSynced()
	}
}

// applyDiff reconciles the projection with one full snapshot: the
// cache's current view is the diff base (origin-confirmed entries the
// feed does not list are deleted — the feed is authoritative, fail
// closed). An empty diff skips the write entirely so the cache's
// revision only moves when state moves.
func (s *Syncer) applyDiff(resp FeedResponse) {
	snap := s.cache.Get()
	want := activeEntries(resp, s.clock())
	deletes := make([][32]byte, 0)
	upserts := make(map[[32]byte]KeyEntry, len(want))
	for h := range snap.V {
		if _, ok := want[h]; !ok {
			deletes = append(deletes, h)
		}
	}
	for h, entry := range want {
		if existing, ok := snap.V[h]; !ok ||
			existing.PrincipalID != entry.PrincipalID ||
			existing.EngineCredential != entry.EngineCredential {
			upserts[h] = entry
		}
	}
	if len(deletes) == 0 && len(upserts) == 0 {
		s.projectAllowlist(resp)
		return
	}
	s.cache.Apply(Revision(resp.Rev), upserts, deletes)
	s.projectAllowlist(resp)
}

// projectAllowlist reconciles the model-permission projection: every
// active principal's effective permissions expand against the local
// catalog (control/domain.ExpandModels — the model:use:* wildcard's
// evaluation point), and the allowlist cache receives per-subject
// upserts. Subjects that vanish from the feed receive an empty model
// list, which ModelAllowed denies (fail closed) — the entry itself is
// harmless to keep, and the full-map ReplaceAll the alternative would
// need buys nothing at phase-1 subject counts. Idempotent: identical
// snapshots re-emit identical upserts and the revision guard settles
// them as no-ops.
func (s *Syncer) projectAllowlist(resp FeedResponse) {
	if s.allowlist == nil {
		return
	}
	current := s.allowlist.Get().V
	seen := map[string]bool{}
	for _, p := range resp.Principals {
		if p.Status != feedActive {
			continue
		}
		models := domain.ExpandModels(p.Permissions, s.catalog)
		seen[p.ID] = true
		if existing, ok := current[p.ID]; !ok || !equalStrings(existing, models) {
			s.allowlist.Upsert(Revision(resp.Rev), p.ID, models)
		}
	}
	for subject := range current {
		if !seen[subject] {
			s.allowlist.Upsert(Revision(resp.Rev), subject, nil)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// activeEntries keeps only the active keys — the projection never
// retains revoked rows; their absence is the deletion. The credential
// rides the row as projected (empty = unprovisioned).
func activeEntries(resp FeedResponse, now time.Time) map[[32]byte]KeyEntry {
	out := make(map[[32]byte]KeyEntry, len(resp.Keys))
	for _, k := range resp.Keys {
		if k.Status == feedActive {
			out[k.Hash] = KeyEntry{PrincipalID: k.PrincipalID, Status: feedActive, EngineCredential: k.EngineCredential, SyncedAt: now}
		}
	}
	return out
}

// Healthy reports whether a sync completed within staleAfter — the
// operator's signal that the projection may be lagging behind the
// authority. Never-synced is unhealthy by construction.
func (s *Syncer) Healthy() bool {
	last := s.lastSyncAt.Load()
	if last == 0 {
		return false
	}
	return s.clock().Sub(time.Unix(0, last)) < s.staleAfter
}

func (s *Syncer) markSynced() {
	s.lastSyncAt.Store(s.clock().UnixNano())
}

// sleepCtx waits for d or the context; it reports whether the wait
// completed (false = ctx ended).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
