---
name: backlog
description: Route every idea by readiness — decided or clearly scoped work files an issue and starts the normal flow immediately; not-yet-started work files an issue labeled status/parked. There is no parking-lot issue; the label is the single parking mechanism.
---

# Backlog

There is no parking-lot issue — lot 1 (#21) sat empty for its whole
life and retired (#62). `status/parked` is the single mechanism for
work that is real but not started: queryable, on the normal issue
lifecycle, no rotation machinery.

## Routing

1. **Decided or clearly scoped?** File an issue and start the normal
   flow (branch → PR) — subject to the component capacity rule: when a
   component already carries a release cycle's worth of implementable
   work, file the issue and park it instead of starting it. Never hold
   decided work in unlabeled limbo.
2. **Real but not starting now?** File the issue anyway and label it
   `status/parked`: scoped enough to describe, deliberately waiting —
   no owner, no cycle capacity, needs discussion first. A sentence or
   two suffices; readiness is judged once, at intake.
3. **Not even idea-shaped?** Make it idea-shaped in a sentence and file
   it with `status/parked`. The tracker is the intake; nothing waits
   outside it.

## Parking hygiene

A parked issue decays without re-affirmation. At the monthly
housekeeping glance (k8s stale semantics, deliberately manual — no
bot): re-affirm what still matters by commenting or removing the
label; close what has gone quiet. Parking is a promise to revisit,
not a shelf to forget.

## Why one mechanism

A parking-lot issue made parked work invisible to queries and needed
its own automation to stay honest. The label is queryable across the
tracker, shares the issue lifecycle, and turns "how do we park work?"
into a one-word answer.
