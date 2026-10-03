---
number: 31
title: "deferred-closed issues, intake-time re-entry"
date: "2026-10-03"
updated: "2026-10-03"
status: "accepted"
supersedes: []
adopted-at: "2026-10-03"
---

# AD-31 — deferred-closed issues, intake-time re-entry

## Context

AD-26 made `status/parked` the single parking mechanism, and the
backlog skill added a monthly re-affirmation glance on top. The
maintainer rejected both: a pile of open issues with no trigger date,
no owner, and no conclusion is tracker rot, and a periodic sweep to
reap it is a correction layer — prohibited by AD-30. An issue's
conclusion must be one of: fixed, feature delivered, solidified, or
deferred. (AD-30's clarification (a), which kept the parked-issue
re-affirmation glance, is void as of this decision — the remainder of
AD-30 stands; AD records are immutable except status flips, so the
narrowing is recorded here.) Research backed the instinct: [Shape Up, "Bets, Not
Backlogs"](https://basecamp.com/shapeup/2.1-chapter-07) discards
unbet work and re-pitches it at intake time ("No backlogs. Backlogs
are a big weight we don't need to carry."); k8s's culture is
"closing is not rejection — reopening is cheap." VS Code's
backlog-candidate voting does not fit a single-maintainer project;
mechanical stale-bots were already rejected with AD-30.

## Decision

The parking mechanism of AD-26 is replaced (the two-plane separation
of AD-26 stands). There is no parked state:

- **Deferring = closing now, with a trigger.** Deferred issues close
  at deferral time, labeled `kind/deferred`, and the closing comment
  carries: what was decided, where it is solidified (AD-N / doc
  path), and `Re-enter when: <concrete trigger>`. "When someone has
  time" is not a trigger and means the work is dropped (AD-30: no
  correction layer will rediscover it).
- **Re-entry rides the intake event.** Creating a new issue or
  planning new work includes the standard search — `is:closed
  label:kind/deferred` plus topic keywords — and matched triggers
  reopen the issue (scope unchanged) or spawn a new one referencing
  it (scope changed). Intake is the one event the flow guarantees,
  so no schedule, sweep, or re-affirmation cycle exists or is
  needed.
- **The monthly glance is deleted along with the label.** Nothing
  on a cron corrects tracker residue.

## Consequences

- The backlog skill is rewritten around routing + deferral +
  intake-time re-entry; issue-lifecycle gains `closed-deferred`;
  AGENTS.md's parked references now say deferred.
- `kind/deferred` replaces `status/parked`; #79 and #82 are the
  first converted instances.
- Open issue lists become honest: everything open is active or
  next-in-line. Deferred knowledge stays findable (closed issues
  and comments are indexed) without pretending to be alive.
- The intake-time check is a procedure, not a gate; if it is
  skipped, the cost is bounded (a deferred item is re-proposed
  later or not at all) — acceptable under AD-30 rather than a
  reason to build a reaper.
