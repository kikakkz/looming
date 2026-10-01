---
name: backlog
description: Route every new idea by readiness — decided or clearly scoped work files a real issue and starts the normal flow immediately; only ideas not ready to become work park in the backlog issue (#21). Use whenever an idea, request, or observation arrives that is not yet tracked.
---

# Backlog

The backlog (#21) is an intake, not a waiting room for everything.

## Routing

1. **Decided or clearly scoped?** File a real issue and start the normal
   flow (branch → PR). Never park decided work — the lot is for ideas
   *not ready to become work*; parking settled questions wastes triage
   attention and hides them from the tracker where CI and reviews can
   see them.
2. **Direction unclear, needs discussion, or no owner yet?** Park it in
   #21: one or two sentences, no design required. Reference stable
   identifiers only (issue / AD numbers), never file anchors.
3. **Graduation.** When a parked idea becomes a real issue, check its
   box in #21 and link the issue. The lot never keeps shadows of
   graduated work.

## Why the split is readiness, not quality

Every idea is welcome at the door — the backlog admits all of them. What
decides where an idea waits is only whether it is ready to become work
today. Filter at graduation (does a real issue exist?), not at intake.
