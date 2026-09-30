---
updated: 2026-09-30
type: activeContext
---

# Active Context

## Current focus

Methodology adoption for the engineering layer: issues #24 (DDD), #25
(architecture constraints), #26 (TDD + complexity budgets in CI), #27
(testing stack). Inputs: the pins from #12 plus the behavior notes in
#21. Bootstrap is complete; the review gate runs on CodeRabbit formal
reviews (#11) with auto-merge.

## Next (in order, each gated by maintainer instruction)

1. Work #24–27 through the normal IDD loop; conclusions land as ADRs via
   `adr_manager.py` and constraint text in `.ai/AGENTS.md` / CI config.
2. **Gateway component** — frozen until explicitly started (AD-3). First
   component; Go; preceded by the agentgateway (AAIF) research (AD-17)
   and the runtime bake-off spike per AD-15.
3. Event-stream schema — deferred by decision; designed through its own
   issue when the gateway work unfreezes.

## Open questions

- Repo layout under `cmd/` per component — first decided with the
  gateway issue.
- Clean first-pass PRs and the 1-approval gate (#21): wait for the
  changes-requested → fix cycle to yield APPROVED, or redesign the gate
  in #23 (pr-watch).
