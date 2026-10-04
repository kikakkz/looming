---
number: 34
title: "polyglot component layout"
date: "2026-10-04"
updated: "2026-10-04"
status: "accepted"
supersedes: []
adopted-at: "2026-10-04"
---

# AD-34 — polyglot component layout

## Context

Slice 0 (#99/#100) placed the Go module at the repo root (`go.mod`,
`cmd/`, `internal/`), implicitly organizing the whole repo as a Go
project. The product is a bundle of components whose future members
(agentruntime, registry, CI orchestration, IM surfaces) are not
committed to Go — several are likelier in Rust or TypeScript. Go
toolchain conventions squatting at the root would force a painful
migration later, and the maintainer's per-component release decision
(混合式 tagging) already treats components as independent build and
release units. Surveying mature polyglot repos (OpenTelemetry's
proto-as-contract, Argo, Grafana, VS Code, and the current polyglot
monorepo literature) confirms one dominant pattern at our scale:
language claims a subtree; the root holds only orchestration.

## Decision

Five rules govern the repository layout:

1. **Each component owns a top-level directory with its native
   project.** Go components carry one `go.mod` inside their directory
   (`gateway/go.mod` today); Rust components will carry `Cargo.toml`;
   TypeScript ones `package.json`. AD-23's structure (four-package
   capabilities, `cmd/` wiring, the dependency matrix, budgets, test
   layers) is component-scoped law, unchanged in content.
2. **`platform/<lang>/` is the language-internal shared home, and it
   only exists at two or more same-language consumers** (the same
   contract-from-N-implementations discipline as AD-27 §2). Until
   then shared code lives inside its first consumer.
3. **`contracts/` — language-neutral IDL (protobuf / JSON Schema /
   OpenAPI) — appears when a second language needs a shape the first
   already owns** (the records-plane event shapes are the named first
   candidate). Generated code flows one way (contracts →
   `platform/<lang>` → components); a schema compatibility gate in CI
   is the cross-language enforcement of the matrix principle.
4. **The matrix principle is bilingual in enforcement**: within a
   language by its own mechanism (go-arch-lint today; cargo-deny /
   workspace lints for Rust; eslint boundaries for TS), across
   languages by the schema gate.
5. **The root stays language-neutral**: Makefile meta-gates (each
   language target skips when its toolchain is absent — the pattern
   `lint-go` established), `docs/`, `.ai/`, `.github/`, and the
   decision records. Nothing language-specific at root.

Deferred, with named triggers: `go.work` when a second Go component
lands; mise (or equivalent) toolchain pinning when a second language
lands; affected-target CI when component count makes full gates slow;
changesets-class polyglot versioning when per-component releases get
automated.

## Consequences

- #102 moved `go.mod`, `cmd/`, `internal/`, and the three Go configs
  under `gateway/`; the module path is now
  `github.com/kikakkz/looming/gateway`.
- New components extend the Makefile gate with their own
  skip-when-absent targets and the AGENTS.md command list in the same
  PR (check-docs contract).
- The per-component release flow tags component subtrees
  (`gateway/v0.1.0`), matching rule 1's project boundaries.
