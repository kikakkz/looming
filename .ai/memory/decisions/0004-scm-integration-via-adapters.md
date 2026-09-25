---
number: 4
title: "SCM integration via adapters"
date: "2026-09-25"
status: "accepted"
supersedes: []
adopted-at: "2026-09-25"
---

# AD-4 — SCM integration via adapters

SCM integration via adapters (repo/issue/MR/webhook/checks). One repo =
one SCM flow. Repo CI stays native (GitLab CI / GitHub Actions); the
platform pipeline is self-built on K8s jobs; CI status is read back
uniformly.
