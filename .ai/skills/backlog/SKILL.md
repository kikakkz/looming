---
name: backlog
description: Route every idea by readiness — decided work starts the normal flow immediately; work that is real but not starting now is filed and closed as deferred with a mandatory Re-enter-when trigger. Re-entry is checked at intake time, never by periodic sweep (AD-31).
---

# Backlog

There is no parking state. An open issue means someone is working it
or it is next in line; everything else is closed. `status/parked` was
retired (AD-31): open-ended waiting is how trackers rot — no trigger
date, no owner, no conclusion.

Methodology is upstream-first: [Shape Up, "Bets, Not
Backlogs"](https://basecamp.com/shapeup/2.1-chapter-07) — unbet work
is discarded and re-pitched at intake; k8s's "closing is not
rejection, reopening is cheap."

## Routing

1. **Decided or clearly scoped?** File an issue and start the normal
   flow (branch → PR) — subject to the component capacity rule: when a
   component already carries a release cycle's worth of implementable
   work, file the issue and **defer it** instead of starting it.
2. **Real but not starting now?** File the issue anyway, then close it
   as **deferred** (see below). A sentence or two suffices; readiness
   is judged once, at intake.
3. **Not even idea-shaped?** Make it idea-shaped in a sentence and file
   it as deferred. The tracker is the intake; nothing waits outside
   it.

## Deferring (closing, with a trigger)

A deferred issue is **closed at deferral time**, labeled
`kind/deferred`, and its closing comment carries exactly:

```
closed-deferred. Solidified at <AD-N / doc path>.
Re-enter when: <the concrete trigger>.
```

The `Solidified at` link is mandatory even for one-sentence ideas: the
issue is a disposable tracker and cannot serve as the durable record.
If the idea is not worth one durable sentence in an AD or doc, it is
dropped (closed-not-planned), not deferred.

The trigger is the contract. "When someone has time" is not a
trigger; "when the PEP shape design starts" is. If the work cannot be
given a trigger, it is not real enough to track — drop it (AD-30: no
compensation layer will rediscover it).

## Re-entry (at intake time, never on a schedule)

Creating a new issue or planning new work **is** the re-entry check:

1. Search closed deferred issues: `is:closed label:kind/deferred`
   plus the topic keywords (comments are indexed — the triggers are
   findable).
2. Trigger matched and scope unchanged → **reopen** the issue, stating
   what changed, and **remove the `kind/deferred` label** (GitHub
   retains labels on reopen; a later completed close must not match
   the intake query as a false deferred).
3. Scope changed → file the new issue and reference the old one; the
   old stays closed (context, not tracker) and **loses the
   `kind/deferred` label** — the new issue now carries the trigger;
   the old one must not match future intake searches.

This rides an event the flow already guarantees — intake — so no
periodic sweep exists or is needed.

## Why no standing backlog

Shape Up: "Backlogs are a big weight we don't need to carry." An open
parked pile hides decay behind a label; a closed deferred set is
honest — everything in it has a conclusion, a home for its knowledge,
and a tripwire for its return.
