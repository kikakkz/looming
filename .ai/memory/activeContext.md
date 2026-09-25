---
updated: 2026-09-25
type: activeContext
---

# Active Context

## Current focus

Repository bootstrap: skeleton, agent-facing conventions (AGENTS.md,
`.ai/`), CI gates, Apache-2.0 + DCO. Bootstrap commit is the single
exception to issue-driven development; everything after it goes through
issues.

## Next (in order, each gated by maintainer instruction)

1. Push skeleton to GitHub and file the bootstrap issue set.
2. **Gateway component** — frozen until explicitly started. First
   component per plan; Go; spec'd by prior decisions (AD-3).
3. agentgateway (AAIF) research — build on it, reference it, or coexist;
   outcome shapes the gateway slot (AD-17).
4. Runtime bake-off spike (smoke opencode + OpenHands → full spike on the
   winner) per AD-15.
5. Event-stream schema — deferred by decision, designed post-bootstrap
   through its own issue.

## Open questions

- Repo layout under `cmd/` per component — first decided with the gateway
  issue.
- Whether to enable CodeRabbit immediately after push (free for public
  repos) — pending maintainer call.
