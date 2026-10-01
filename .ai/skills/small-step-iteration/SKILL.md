---
name: small-step-iteration
description: Decompose large features into small PR-sized slices — one coherent change per PR, branches cut from latest main, gate green at every merge, umbrella issues as checklists. Use when a task smells bigger than a day or two of work, when planning or slicing a large feature, or when a branch has lived longer than two days.
---

# Small-step iteration for large features

A large feature never lands as one branch. It lands as a sequence of
small slices, each merged to main while `make ci-gate` is green. The
per-slice increment cycle (implement → test → verify → commit) lives in
the pinned external skill `incremental-implementation`; this skill is
the slicing discipline around it.

## Principles

1. **One PR, one coherent change.** A reviewer — human or agent — must
   be able to verify it in one sitting. Guideline: ≤ ~400 lines of
   diff, ≤ 2 days of work.
2. **Branch from latest main, merge back fast.** No long-lived feature
   branches. After each squash merge, cut the next slice from the new
   main (rebase, never merge commits, per the trailer check).
3. **The gate stays green at every merge.** A slice that temporarily
   breaks `make ci-gate` is not a slice — split further.
4. **Umbrella issues are checklists, not branches.** An epic issue
   tracks the slices; each slice gets its own issue before any code
   exists (hard constraint 1: no issue, no code).
5. **Every slice has its own verification.** If you cannot state how a
   slice is verified independently — acceptance criteria, tests per
   AD-24 — it is not sliced small enough.

## Slicing heuristics

- **Pure refactors first.** Extract the seam, rename, restructure —
  behavior identical, existing tests as the safety net. New capability
  lands in later slices on the clean seam.
- **Domain and state machines before I/O adapters.** Anything testable
  in memory — domain logic, state machines, parsers (AD-23:
  `domain/` before `adapter/`) — ships before network and disk.
- **Infrastructure before behavior.** The `port/` trait plus one
  trivial implementation before the second real one.
- **Slice by verification boundary**, not by file or by layer.
- **Defer integration.** Two slices that only compose through third
  piece of plumbing mean the plumbing is its own slice.

## Working loop

1. Find or file the issue for the **next slice only** (the umbrella
   lists the rest).
2. Branch `<type>/<issue>-<slug>` from latest main; the prefix matches
   the PR title type (CI enforces both).
3. Implement + verify: red-green-refactor per AD-24, tests in the same
   PR, `make ci-gate` green before push.
4. Open the PR with `Closes #N`, hand it to the pr-watch loop; squash
   merge on approval.
5. Repeat from the new main. If reality reshapes the plan, update the
   umbrella issue — the tracker shows truth, not intent.

## Red flags — stop and re-slice

- The branch is older than two days or has drifted from main.
- The PR mixes refactor + feature, or adds an adapter + its first
  consumer.
- The gate is red "temporarily".
- The PR body needs more than a few lines to explain "what and why".
- You are about to skip trailers, disable a check, or mark a test
  skipped to get CI green.

## Provenance

Adapted from wait-agent's `small-step-iteration` skill, forged during
its relay epic (slices landing as #74, #75, #78 — transport gate,
connection table, stream routing). House rules: hard constraints 1–2,
AD-23 layering, AD-24 TDD and budgets.
