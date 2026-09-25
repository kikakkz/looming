---
number: 14
title: "Architecture principle"
date: "2026-09-25"
status: "accepted"
supersedes: []
adopted-at: "2026-09-25"
---

# AD-14 — Architecture principle

Architecture principle: batteries included, everything swappable.
Pluggable slots behind small versioned interfaces; control plane
(blueprint orchestrator, event log) deliberately NOT pluggable — it is the
product IP.
