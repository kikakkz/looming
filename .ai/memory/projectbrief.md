---
updated: 2026-09-25
type: projectbrief
---

# Project Brief

Looming is an enterprise agent-engineering platform delivered as a single,
self-contained bundle. It gives an organization one control plane for the
whole agent lifecycle:

- **Model gateway** — stateless OpenAI-compatible forwarding with
  governance: API-key auth (enterprise OA later), per-user quota, an
  append-only interaction log, and a pre/post plugin chain whose
  pre-hooks run deterministic rules plus a locally hosted decision model
  for risk interception.
- **Pluggable runtimes** — planner/executor/worker/judge profiles behind
  a runtime provider interface (capability levels L0–L3). Users can adopt
  the bundled default or bring Codex, Kimi Code, goose, and others.
- **Organization knowledge** — unified memory (org/project/user scopes,
  dual-track writes) and a unified registry for MCP servers, skills, and
  tools, with review, approval, and admission scanning.
- **Issue-driven pipelines** — SCM adapters for GitHub and bundled GitLab
  CE; repo CI stays native while the platform runs its own pipeline on an
  isolated, elastic sandbox pool (K8s + gVisor, warm pool).
- **Verifier-first quality** — CI guards known deterministic failures;
  the judge discovers new objective failures (codified into CI) and taste
  findings (promoted into memory/skills/tools).

Core principle: **batteries included, everything swappable**. The bundle
ships our best combination; every slot sits behind a small interface.

## Goals

One install, whole organization; data sovereignty (zero external SaaS
dependency); auditability of every agent action.

## Non-goals

Being the best coding agent (we orchestrate agents, we don't compete with
them); a hosted SaaS offering.
