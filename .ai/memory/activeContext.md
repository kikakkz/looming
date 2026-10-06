---
updated: 2026-10-06
type: activeContext
---

# Active Context

Links-only snapshot — SSoT details live in [[progress]], [[decisions]],
and the tracker. How we got here: the 2026-10-06 entry in [[progress]].

## Open threads (tracker is the truth)

- #114 — slices D (RBAC read side) + E (OIDC) remain.
- #108 — design landed (#128, AD-37); slices CLI-0/1/2 to implement.
- #130, #131 — real bugs surfaced by the bundle e2e suite.
- #105, #123 — flake tracking.

## Landed anchors

- #107 closed (bootstrap/topology plane; leftovers named in the issue).
- AD-36 (topology plane) and AD-37 (CLI) are the latest decisions.
- Bundle e2e suite: tests/e2e — AD-25 e2e layer; `make test-e2e`, own
  CI job, not in ci-gate.

## Standing

- Memory updates ride branch + reviewed PR (AD-22); stable identifiers
  only (#N / AD-N).
- Environment: go1.25 at /opt/data/source/3rd/go-1.25/bin (GOROOT
  unset); GOPROXY=goproxy.cn; GitHub-flaky — retry loops. Full notes
  in the 2026-10-06 [[progress]] entry.
