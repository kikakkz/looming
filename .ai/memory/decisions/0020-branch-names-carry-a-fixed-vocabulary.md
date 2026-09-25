---
number: 20
title: "Branch names carry a fixed-vocabulary prefix"
date: "2026-09-25"
status: "accepted"
supersedes: [19]
adopted-at: "2026-09-25"
---

# AD-20 — Branch names carry a fixed-vocabulary prefix

Branch names carry a fixed-vocabulary prefix: `<type>/<issue>-<slug>`,
where type ∈ Conventional Commits types (the same source of truth as PR
titles). A `branch-name` CI check validates the format and requires the
prefix to match the PR title's type. Amends AD-19's no-prefix stance.
