---
number: 6
title: "All four agent roles get disposable sandboxes; the harness"
date: "2026-09-25"
status: "accepted"
supersedes: []
adopted-at: "2026-09-25"
---

# AD-6 — All four agent roles get disposable sandboxes; the harness

All four agent roles get disposable sandboxes; the harness runs OUTSIDE
the sandbox; per-run minted short-lived repo-scoped credentials. Sandbox
policy: no production credentials, no network egress except allowlist
(SCM + package registries), no production data. Model/gateway credentials
never enter the sandbox. Approval moves to the MR boundary with risk
tiers.
