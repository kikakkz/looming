---
number: 24
title: "TDD workflow and complexity budgets"
date: "2026-10-01"
updated: "2026-10-01"
status: "accepted"
supersedes: []
adopted-at: "2026-10-01"
---

# AD-24 — TDD workflow and complexity budgets

## Context

Issue #26 asked for the TDD discipline and the complexity budgets that
keep a growing Go codebase reviewable by humans and agents alike. The
reference set is the golangci-lint ecosystem defaults plus how mature
Go projects use them: complexity linters as a ratchet, never as a
one-time cleanup. Function-level budgets must be generous enough that
honest first implementations pass, and tight enough that growth is
visible.

## Decision

1. **TDD red-green-refactor** is the working loop: a failing test
   precedes the implementation it drives. Test code lands in the same
   PR as the code it tests (extends hard constraint 2, CI-first).
2. **Complexity budgets**, enforced by `.golangci.yml`:
   - `funlen`: at most 50 statements per function (line count disabled —
     statement count is the honest proxy);
   - `gocognit`: cognitive complexity at most 30 per function;
   - `cyclop`: cyclomatic complexity at most 15 per function.
3. **Ratchet**: budgets only ever tighten, and tightening (e.g.
   gocognit 30 → 15) happens by superseding this decision, never by a
   drive-in config edit.
4. **Suppressions**: `//nolint` requires the specific linter name, a
   written explanation, and must actually suppress something
   (`nolintlint` with `require-specific`, `require-explanation`,
   `allow-unused: false`). Every suppression is visible debt in review.
5. **Base linter set**: golangci `standard` plus `gosec`, `misspell`,
   and the complexity linters above; `gofmt` and `goimports` as
   formatters. Additions to the enabled set are a PR with rationale.

## Consequences

- `.golangci.yml` lands in this PR; the CI job that runs it lands with
  the first Go component. Local `make lint-go` warns and skips until
  then.
- The initial budgets are deliberately lenient. Expect the first real
  component to trigger exactly the conversations this record exists to
  host: extract, or justify a targeted `//nolint`.
- Review energy shifts from style arguments to budget conformance;
  `nolint` comments carry the exceptions in-band.
