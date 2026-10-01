---
number: 25
title: "Testing stack policy: layers, tags, coverage"
date: "2026-10-01"
updated: "2026-10-01"
status: "accepted"
supersedes: []
adopted-at: "2026-10-01"
---

# AD-25 — Testing stack policy: layers, tags, coverage

## Context

Issue #27 asked for the full testing stack: unit, module/integration,
system, regression, visual. Reference set: testcontainers-go (container-
backed integration tests), go-test-coverage (threshold gates), gotestsum
(reporting), and the standard Go convention of build tags for slow
layers. Reality check: there is no Go code yet, no runner budget for
full system tests, and no UI to screenshot — so the policy must name
what exists now and defer what does not, without leaving the deferred
parts undefined.

## Decision

1. **Layers and build tags**:
   - **Unit** — default build, no tag. No network, disk, or wall-clock
     dependencies; pure functions and domain logic.
   - **Integration** — `-tags integration`. Real dependencies via
     testcontainers-go; GitLab runs from the `gitlab/gitlab-ce` image
     through `GenericContainer` (testcontainers-go ships no GitLab
     module). Containers start in 3–5 minutes, so these stay lean.
   - **System / E2E** — `-tags e2e`. **Deferred** until a runner budget
     exists; the tag and the layer are defined now so code can opt in.
   - **Regression** — not a separate runner: regression protection is
     replay/fixture-based tests living in the unit and integration
     layers, added with every bug fix.
   - **Visual** — out of scope until the platform has a UI.
2. **Frameworks**: `testify` + `go-cmp`. Table-driven tests are the
   default shape; a heavier framework (ginkgo-style) is reconsidered
   only when table tests demonstrably strain.
3. **Coverage**: ≥ 80% at file, package, and total granularity via
   go-test-coverage (`.testcoverage.yml`). The gate joins CI with the
   first Go component; until then `make test-coverage` reports without
   failing.
4. **Flake policy**: a flaky test gets a `kind/flake` issue; a skip
   must cite the issue number in-code. Skips without a cited issue fail
   review.
5. **Test-only code** lives under `tests/` (fixtures, helpers, fakes).
   Production packages never import it.
6. **Tooling**: gotestsum for human-readable runs locally and in CI;
   `-race` always on. Fuzzing is reserved for gateway parsers
   (HTTP/proxy parsing) when that component lands.

## Consequences

- `.testcoverage.yml` and the `test-unit` / `test-coverage` make targets
  land now; the CI job enforcing tags and thresholds lands with the
  first Go component.
- CI duration budget: unit seconds, integration minutes, e2e deferred —
  each layer's cost is visible in the tag that selects it.
- Every bug fix carries its regression test in the same PR; "regression
  suite" is a property of the other layers, not a separate system to
  maintain.
