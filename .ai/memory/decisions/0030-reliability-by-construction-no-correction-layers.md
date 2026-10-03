---
number: 30
title: "reliability by construction, no correction layers"
date: "2026-10-03"
updated: "2026-10-03"
status: "accepted"
supersedes: []
adopted-at: "2026-10-03"
---

# AD-30 — reliability by construction, no correction layers

## Context

The issue-lifecycle work (#90) initially paired a merge-time checklist
with a monthly sweep: merged-PR residue would be reaped at the
periodic housekeeping glance. The maintainer rejected the sweep on
principle: the issue-driven flow — create, implement, test, MR, close —
must itself be reliable end to end. A periodic correction pass is an
admission that the flow leaks, and correction layers are rot: they
hide the leak's true location, add machinery whose own reliability
must then be maintained, and normalize sloppiness upstream ("the
sweeper will catch it"). This is the same family as the fail-closed
PEP and the "no silent drops" pattern — failures own their handling
at the point where they occur.

## Decision

Reliability is by construction at the points where failures occur;
compensation layers are prohibited as a substitute.

For issue closure specifically, the defense stack is:

1. **At creation, prevention** — PR bodies classify every issue
   reference: `Closes #N`, or `Refs #N` with the stay-open reason
   stated. Bare silent `Refs` is out of process; a body lint in CI
   is planned to make it mechanically detectable before merge
   (follow-up issue, not yet enforced).
2. **At merge, closure is part of the action** — the merge is not
   complete until every referenced issue is processed: `Closes`
   issues auto-close; each justified `Refs` is immediately closed
   with its closing comment or re-affirmed with its reason. The
   merger owns this, in the same operation; there is no later queue.
3. **No periodic reaper** — nothing on a cron corrects flow residue.
   If something slips past 1–2, that is a bug in the flow, fixed at
   its root cause, not janitorial work.

Two clarifications. (a) The parked-issue re-affirmation glance stays:
parked work has no completion event to attach to, so periodic
re-affirmation is its lifecycle, not a correction layer. (b)
Pre-merge gates (ci-gate, check-docs, co-change) remain — prevention
at the door is construction, not compensation.

## Consequences

- The issue-lifecycle skill carries the merge-time checklist; PR #91
  removed the sweep that this AD supersedes as an approach.
- A CI PR-body lint (Refs needs a stated reason) is filed as a
  follow-up implementation issue — prevention moved into the gate.
- This principle applies to every flow in the system (event
  backbone: no silent drops; PEP: fail closed). When a future design
  proposes a periodic cleanup/correction mechanism, this AD is the
  standing challenge: make the producing step reliable instead.
