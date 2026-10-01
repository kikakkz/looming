---
number: 23
title: "Go component structure: DDD layers and context map"
date: "2026-10-01"
updated: "2026-10-01"
status: "accepted"
supersedes: []
adopted-at: "2026-10-01"
---

# AD-23 — Go component structure: DDD layers and context map

## Context

Issues #24 (DDD practice) and #25 (architecture constraints) asked what
design and module-design rules Go components must follow, under the
standing rule that this repository adopts mature upstream practice
instead of inventing its own. Reference set: ThreeDotsLabs wild-workouts
(hexagonal DDD in Go), go-kratos/kratos (biz/data/service layering), and
standard Go monorepo layout. No Go code exists yet, so every structural
decision here is a hypothesis the gateway component will validate first.

## Decision

1. **Module granularity**: one root `go.mod` at the repository root.
   Per-component modules are deferred until a component actually needs an
   independent release cadence; premature splitting costs more than it
   saves at this size.
2. **Per-component layering** (wild-workouts style): every capability
   lives under `internal/<capability>/` with four packages —
   `app/` (use-case orchestration), `domain/` (pure model and business
   rules), `port/` (interfaces), `adapter/` (driving and driven
   implementations). `cmd/<binary>/` holds only `main` wiring and may
   import anything.
3. **Domain purity**: `domain/` packages import the standard library
   only, enforced mechanically by the depguard rule in `.golangci.yml`.
4. **V0 context map** — five bounded contexts, all independent:
   `internal/gateway/dp` (data plane: forward/proxy), `internal/gateway/cp`
   (control plane: keys, quotas, model governance), `internal/cicd`
   (pipeline orchestration), `internal/agentruntime` (pluggable
   agent/provider host), `internal/registry` (memory & tools registry).
5. **Dependency rules** between components are declared in
   `.go-arch-lint.yml`; any dependency not listed there is rejected by
   the linter. V0 declares no cross-component dependencies.
6. **Extending the map**: a new capability or a new allowed dependency
   lands in the same PR as a superseding decision record and the
   `.go-arch-lint.yml` update.

## Consequences

- `.go-arch-lint.yml` and `.golangci.yml` land now; the CI job that runs
  them lands with the first Go component (hard constraint 2,
  CI-first). Until then both configs are prospective and unenforced.
- The context map is the starting hypothesis, not settled truth: the
  gateway implementation may split `gateway/dp` further, but only via a
  superseding AD.
- Code review no longer argues about where a file belongs: the layers
  and the dependency matrix are lintable.
