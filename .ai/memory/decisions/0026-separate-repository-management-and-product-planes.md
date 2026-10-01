---
number: 26
title: "Separate repository-management and product planes"
date: "2026-10-01"
updated: "2026-10-01"
status: "accepted"
supersedes: []
adopted-at: "2026-10-01"
---

# AD-26 — Separate repository-management and product planes

## Context

The tracker was mixing two domains. The **repository-management plane**
is how the Looming repository manages its own development: the `.ai/`
skills and tools, `make ci-gate`, pr-watch, the semgrep rule pack, the
memory bank, and the deferred judge work (#20) — an agent that reviews
this repository's own work. The **product plane** is what the bundle
ships to organizations: the model gateway, the agent runtime (the
pluggable layer where customers attach codex/claude/kimi-class agents),
the memory & tools registry, and CI orchestration **for customer
organizations**. #48 first conflated the two (survey items for product
components sat in an issue about repo tooling); maintainer review
separated them (#48 re-scoped, product items moved to #54). Within the
product plane, a second confusion was also named: the product's CI
orchestration capability is not this repository's own CI, which is
live, separate, and belongs to the repository plane.

## Decision

1. Every issue, decision record, skill, and gate declares its plane.
   Items that span both get split at intake; neither plane may absorb
   the other's work silently.
2. Product component work lands in per-component issues under the
   AD-23 context map. Holding lists (like #54) may bridge until the
   components exist; they distribute into component issues at
   unfreeze time, not before.
3. This repository's own CI (`ci.yml`, `ci-gate`) is repository-plane
   tooling. The product's CI orchestration is designed with the
   product and shares no machinery with it.
4. The judge that reviews this repository (#20) is repository plane.
   The judge role inside the product's multi-agent orchestration is a
   product concept; the two share a name only.

## Consequences

- The architecture discussion runs per plane: product component
  design never blocks on, nor hides inside, repo-management work.
- New tooling PRs state their plane in the body; reviewers reject
  plane conflation the same way they reject missing tests.
- When the product outgrows this repository, the planes split into
  separate trackers or repos (the k/enhancements and test-infra
  pattern); this record is the seam along which that future split
  runs.
