---
updated: 2026-10-01
type: activeContext
---

# Active Context

## Current focus

- Go engineering methodology adopted: AD-23 (DDD structure + context
  map), AD-24 (TDD + complexity budgets), AD-25 (testing stack) via
  #33–#35 / PR #36; research issues #24–#27 and tool issue #32 closed.
  Checkable contract now lives in root AGENTS.md "Go engineering
  standards"; Go CI jobs land with the first Go component.

## Next (each gated by maintainer instruction)

- Gateway component (frozen until started): AD-3; preceded by the
  agentgateway research and the runtime spike (AD-15). Methodology
  configs (.golangci.yml, .go-arch-lint.yml, .testcoverage.yml) are
  already in place waiting for it.
- Event-stream schema: deferred by decision; its own issue when
  unblocked.

## Open questions

- E2E runner budget — the `e2e` build tag is defined (AD-25) but the
  layer stays empty until one exists.
- Clean first-pass PRs vs the 1-approval gate — tracked in #21;
  redesign candidate under #23 (pr-watch).
