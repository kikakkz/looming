---
updated: 2026-10-01
type: activeContext
---

# Active Context

## Current focus

- Engineering-tooling suite complete — see the 2026-10-01 entry in
  [[progress]]: semgrep rule pack (#19), pr-watch loop owner (#23),
  upstream-first skill with `make check-skills` (#39). The repo now
  enforces its own review conventions mechanically.

## Next (each gated by maintainer instruction)

- Overall architecture discussion with the maintainer before any
  feature work — the gateway stays frozen (AD-3) until that
  conversation lands; the agentgateway research and runtime spike
  (AD-15) precede it.
- Event-stream schema: deferred by decision; its own issue when
  unblocked.

## Open questions

- E2E runner budget — the `e2e` layer is defined in AD-25 but stays
  empty until one exists.
- Review-gate economics at scale — five-round cleanups on #41/#44 were
  worth it for new tooling; the "clean PR needs an explicit approval
  request" step is the documented lever until the gate is redesigned
  (#21 notes, pr-watch implements).
