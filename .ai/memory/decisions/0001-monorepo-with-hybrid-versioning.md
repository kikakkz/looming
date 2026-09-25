---
number: 1
title: "Monorepo with hybrid versioning"
date: "2026-09-25"
status: "accepted"
supersedes: []
adopted-at: "2026-09-25"
---

# AD-1 — Monorepo with hybrid versioning

Monorepo with hybrid versioning: components carry independent semver
image tags; a bundle release is a git tag plus a manifest pinning every
component's exact version; only changed components are published.
