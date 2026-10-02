# AGENTS.md

Looming is an enterprise agent-engineering platform shipped as a single
self-contained bundle. This file is the operating contract for every
contributor, human or AI. It is optimized for constraints and commands;
bulky knowledge lives in `docs/` and is referenced by pointer.

## Hard constraints (non-negotiable)

1. **Issue-driven development.** No issue, no code — and "no issue yet"
   never means skip: before any development, find or file the issue first
   (bots and agents too; CI cannot see the tracker, so this is on the
   contributor). Every PR references an issue (`Closes #N`). Branches are
   named `<type>/<issue>-<slug>` where `<type>` is one fixed vocabulary —
   the Conventional Commits types
   (feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert) — and the
   prefix must match the PR title's type (CI enforces both). The prefix
   follows the *change type*, not the issue's `kind/*` label: a
   feature-kind issue whose change only touches CI is `ci/N-...` with a
   `ci:` title (e.g. `ci/9-coderabbit-review-gate`; also
   `feat/6-memory-housekeeping`, `fix/1-...`). The issue thread carries
   plan, status, and review artifacts.
2. **CI-first.** Every code change lands in the same PR as the CI that
   checks it. Red CI never merges. Run `make ci-gate` locally before
   pushing.
3. **Attribution.** Humans sign DCO (`git commit -s`); AI assistance is
   disclosed with `Assisted-by:` / `Generated-by:` trailers. `Co-Authored-By:`
   is banned for AI. AI never signs `Signed-off-by`.
4. **License.** Apache-2.0. New source files carry the SPDX header:
   `# SPDX-License-Identifier: Apache-2.0`.
5. **Never commit secrets.** Credentials, tokens, and private keys do not
   enter the repository, ever. Report leaked secrets by opening a security
   advisory, not an issue.
6. **Generated artifacts are read-only.** Anything produced by codegen or
   automation is regenerated, never hand-edited.
7. **Process authority.** If an external methodology pack (e.g. Superpowers)
   disagrees with this file on *process*, this file wins. Methodology packs
   govern implementation craft; this file governs how the repository works.
8. **Stack kits.** Introducing a new language, framework, or design requires
   landing its supporting kit in the same PR: CI checks, the skills that
   encode its conventions, and updates to this file. A bare dependency
   addition is not mergeable.

## Commands

- `make ci-gate` — run the full local gate (branch name, locks, tool
  tests, trailer checks, ADR check, shell lint, semgrep rules).
- `make check-branch` — validate the current branch name against the
  naming rule before push (same rule as the `branch-name` CI check). Run
  `python3 .ai/tools/check_branch_name.py --title "ci: ..."` to also
  check prefix/title consistency.
- `make validate-locks` — validate `.ai/*.lock.toml` files and the memory
  bank.
- `make test-tools` — unit tests for `.ai/tools/`.
- `make check-trailers` — validate commit-message trailers on `HEAD`.
- `make check-adr` — validate decision-record links and index freshness.
- `make check-docs` — doc-repo consistency: documented make targets
  exist, ci-gate prerequisites are named here, skill counts and memory
  wikilinks resolve.
- `make lint-sh` — shellcheck over the repo scripts.
- `make check-skills` — validate every `.ai/skills/*/SKILL.md` against
  the Agent Skills frontmatter contract.
- `make check-index` — validate `.ai/index.yaml`: parseable under the
  constrained subset, no unknown sections, every anchor (files, dirs,
  rule pointers) exists.
- `make lint-semgrep` — validate the custom rule fixtures
  (`semgrep --test`) and scan `.ai/tools` with the rule pack
  (`.ai/semgrep/rules`); warns and skips when semgrep is missing locally.
- `make lint-go` — golangci-lint (complexity budgets, AD-24); warns and
  skips when golangci-lint or go.mod is missing locally.
- `make test-unit` — Go unit tests with `-race`; falls back to plain
  `go test` without gotestsum; skips when go.mod is missing.
- `make test-coverage` — coverage profile plus the go-test-coverage
  threshold check (`.testcoverage.yml`, AD-25) when installed.

## Go engineering standards

Applies to all Go components. Rationale and evidence live in the decision
records; this section is the checkable contract. Enforcing CI jobs land
with the first Go component (CI-first rule); configs are already in the
repo.

**Structure (AD-23).** One root `go.mod`. Every capability lives under
`internal/<capability>/` in four packages: `app/` (use-case
orchestration), `domain/` (pure model and business rules), `port/`
(interfaces), `adapter/` (driving and driven implementations).
`cmd/<binary>/` holds only `main` wiring.

**Architecture (AD-23).** The bounded contexts and their dependency
matrix are declared in `.go-arch-lint.yml`; dependencies not listed
there are rejected by the linter. V0 contexts: `gateway/dp`,
`gateway/cp`, `cicd`, `agentruntime`, `registry` — all independent.
Extending the map requires a superseding AD in the same PR.

**TDD and complexity (AD-24).** Red-green-refactor; the failing test
lands before the implementation, in the same PR. Budgets enforced by
`.golangci.yml`: ≤ 50 statements per function (funlen), gocognit ≤ 30,
cyclomatic ≤ 15. Budgets only ratchet down, via a superseding AD-24.
Every `//nolint` names its linter, explains itself, and must be in use
(nolintlint).

**Testing (AD-25).** Layers by build tag: unit (default — no network,
disk, or wall-clock), `integration` (testcontainers-go; GitLab CE via
`GenericContainer`), `e2e` (deferred until a runner budget exists).
Regression protection is replay/fixture tests inside the unit and
integration layers, added with every bug fix. Assertions: testify +
go-cmp. Coverage ≥ 80% at file, package, and total granularity
(go-test-coverage, `.testcoverage.yml`). A skipped test cites its
`kind/flake` issue number; test-only helpers live under `tests/` and
are never imported by production code.

## Directory map

- `cmd/` — component entrypoints (one directory per binary; empty until the
  gateway issue lands).
- `docs/` — long-form knowledge. Index: [docs/README.md](docs/README.md).
- `.ai/` — agent assets: skills, external skill pins, MCP server pins, repo
  tools, memory bank. Rules: [.ai/AGENTS.md](.ai/AGENTS.md). Bootstrap
  map: [.ai/index.yaml](.ai/index.yaml) — read it first, never crawl.
- `.github/` — templates, CODEOWNERS, workflows, contributing policy.
- `.ai/memory/decisions.md` — pointer; the decisions themselves live one per
  file in `.ai/memory/decisions/` (generated index: `decisions/README.md`).
  Read them before proposing anything architectural.

## Collaboration protocol

- Work starts at the issue tracker: find or file the issue before writing
  any code, then cut the branch from it.
- Work happens in issues; plans are posted as issue comments before
  implementation for anything non-trivial.
- Acceptance criteria in the issue define "done"; PRs state how each is met.
- Reviews judge conformance to this file as much as code quality.
- Product-plane issues state the `.ai/` tooling they will replace or
  promote (dogfood migration target — e.g. pr_watch.py -> product
  judge); AD-26's seam is a literal migration checklist.
- Dogfood automation failure is issue-worthy: when a repo automation
  (ci-gate job, pr-watch, housekeeping, labeler) flakes or misbehaves,
  file an issue instead of silently retrying.
- Design-first trigger: a change that is user/operator-facing or
  crosses an AD-23 context boundary lands as `kind/design` with an AD
  before code; everything else is a plain issue.
- Product intake backpressure: a component carries at most as many
  open implementable issues as one release cycle can land; excess is
  labeled `status/parked` (issue-shaped work waits as a parked issue).
- **Review-finding triage.** Every review finding must end in exactly one
  of three buckets: a **codified rule** (landed as a semgrep rule with
  positive/negative fixtures in `.ai/semgrep/rules`, AD-8), a **documented
  rule** (encoded in a skill or `docs/`, for findings too semantic for
  static matching), or an **accepted residual** (the reason it needs no
  action is stated in the PR or issue thread). A finding may not be
  dropped without one of these.
- Methodology defaults are upstream-first: with no house rule, adopt
  mature upstream practice and cite it
  ([.ai/skills/upstream-first/SKILL.md](.ai/skills/upstream-first/SKILL.md));
  escalate when no good reference exists, references conflict
  materially, or the decision is organization-novel.
- Ideas route by readiness: decided work files an issue and starts;
  not-yet-started work files an issue labeled `status/parked` — one
  parking mechanism, no open-ended tracking issues
  ([.ai/skills/backlog/SKILL.md](.ai/skills/backlog/SKILL.md)).
- Issues are disposable trackers with a mandatory close: durable
  output is solidified and linked first, then the issue closes — no
  holding lists, no perpetual open issues
  ([.ai/skills/issue-lifecycle/SKILL.md](.ai/skills/issue-lifecycle/SKILL.md)).
- Design work at any level runs the same top-down domain method —
  L0 context map, L1 module design, L2 hexagonal component — before
  code starts
  ([.ai/skills/domain-design/SKILL.md](.ai/skills/domain-design/SKILL.md)).
- Bots and agents follow the same rules as humans: same CI, same trailer
  policy, same issue protocol.

## Versioning and releases

- Conventional Commits, enforced on PR titles (squash merge).
- Hybrid versioning: components carry independent semver image tags; a
  bundle release is a git tag plus a manifest pinning every component's
  exact version. Only changed components are published per release.
- Release details: [.ai/skills/release/SKILL.md](.ai/skills/release/SKILL.md).

## Bootstrap exception

The initial bootstrap commit predates the issue tracker and is the single
exception to rule 1. Everything after it is issue-driven.
