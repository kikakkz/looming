---
updated: 2026-09-30
type: activeContext
---

# Active Context

## Current focus

- Methodology adoption: #24 (DDD), #25 (architecture constraints),
  #26 (TDD + complexity budgets), #27 (testing stack) — inputs from #12
  pins and the review-gate notes in #21.

## Next (each gated by maintainer instruction)

- Gateway component (frozen until started): AD-3; preceded by the
  agentgateway research (AD-17) and the runtime spike (AD-15).
- Event-stream schema: deferred by decision; its own issue when
  unblocked.

## Open questions

- `cmd/` per-component layout — decided with the gateway issue.
- Clean first-pass PRs vs the 1-approval gate — tracked in #21;
  redesign candidate under #23 (pr-watch).
