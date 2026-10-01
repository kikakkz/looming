---
name: backlog
description: Route every new idea by readiness — decided or clearly scoped work files a real issue and starts the normal flow immediately; only ideas not ready to become work park in the current backlog lot. Use whenever an idea, request, or observation arrives that is not yet tracked.
---

# Backlog

The backlog is a rotating parking-lot issue (one lot at a time; the
current lot's number is named exactly once, in the rotation section
below) — an intake, not a waiting room for everything.

## Routing

1. **Decided or clearly scoped?** File a real issue and start the normal
   flow (branch → PR). Never park decided work — the lot is for ideas
   *not ready to become work*; parking settled questions wastes triage
   attention and hides them from the tracker where CI and reviews can
   see them.
2. **Direction unclear, needs discussion, or no owner yet?** Park it in
   the current lot: one or two sentences, no design required. Reference
   stable identifiers only (issue / AD numbers), never file anchors.
   Idea-shaped work waits here; **issue-shaped but not-now work files a
   real issue labeled `status/parked`** instead — parked work must be a
   queryable state across lot rotations (the k8s lifecycle-label
   lesson), and a parked issue re-affirms its parking at every lot
   rotation or decays with the lot.
3. **Graduation.** When a parked idea becomes a real issue, check its
   box in the lot and link the issue. The lot never keeps shadows of
   graduated work.

## The lot has a lifespan

A backlog lot never stays open indefinitely — a lot that cannot close
is a design smell. Rotate when the current lot reaches roughly ten
ungraduated items or about two months without a graduation, whichever
comes first:

1. Close the current lot with a summary comment: what graduated (with
   links), what is carried, what died.
2. Open the successor lot immediately. **Carried ideas are re-stated,
   never copied** — re-affirming an idea is a deliberate act, and ideas
   that earn no re-affirmation die with their lot. That decay is the
   forcing function keeping the lot honest.
3. Point this skill's "currently" reference at the successor — the
   "currently" line below is the single place a lot number is named,
   and nothing else in the repo may hardcode one.

**Current lot: #21 (lot 1, opened 2026-09-26).**

Until automation exists, whoever closes a lot performs the rotation by
hand.

## Why the split is readiness, not quality

Every idea is welcome at the door of the process — none is rejected for
being half-formed. But only ideas not ready to become work wait in the
lot; ready ones never enter it at all (routing step 1). Judge each idea
once, at intake: readiness decides where it waits, and graduation
(does a real issue exist?) or lot rotation retires it.
