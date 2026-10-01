---
updated: 2026-10-01
type: progress
---

# Progress

## 2026-09-25 — Bootstrap

- Architecture and process decisions AD-1 … AD-17 accepted through design
  discussion (see [[decisions]]).
- Repository skeleton created: Apache-2.0 license, root AGENTS.md with
  nested `.ai/AGENTS.md`, DCO + AI attribution policy (Assisted-by /
  Generated-by trailers; Co-Authored-By banned), conventional commits on
  PR titles.
- `.ai/` agent assets: two self-authored skills (conventional-commits,
  release), sha-pinned external skill lockfile
  (superpowers, ponytail, code-review, triage, kubernetes), intentionally
  empty MCP server lockfile, repo tools with tests (validate_locks,
  check-trailers), memory bank initialized.
- CI gate: `make ci-gate` (lock validation + memory bank validation + tool
  unit tests + trailer policy + shell lint) plus PR-title format workflow.
- Pushed to github.com/kikakkz/looming (public, default branch main).
  Network note: direct HTTPS to github.com is flaky from the dev machine;
  token auth via ~/.git-credentials works when connectivity holds.
  First push results: ci and labels workflows both green; kind/* and
  area/* label taxonomy synced to GitHub.

## 2026-09-25 — First IDD loop (issue #1 → PR #2)

- Corrections landed via the full loop: issue #1 (kind/task, area/docs),
  branch `1-bootstrap-corrections`, PR #2. Contents: renamed project to
  **Looming** (was Loom), added `.gitattributes`, added `assets/logo.svg`
  (warp/weft/shuttle motif), README restructured on the wait-agent pattern.
- First PR caught two real defects in bootstrap CI, both fixed on-branch:
  1. `check-trailers.sh` iterated commit bodies line-by-line, so multi-line
     bodies were checked as pseudo-commits; rewritten to per-commit
     inspection via `rev-list` + `mapfile`, errors now name the sha.
  2. The DCO step checked `origin/main..HEAD`, which on PR events is
     GitHub's ephemeral merge commit (never carries trailers) — now checks
     `pull_request.base.sha..head.sha`.
  Regression tests added (7 cases, incl. merge-commit and multi-line-body).
- Maintainer authorized AI commits to carry
  `Signed-off-by: Zhao KK <kikakkz@hotmail.com>` for this session.

## 2026-09-25 — Naming enforcement + protection hardening

- PR #2 merged (squash, 1f914a3). Repo settings: auto-merge allowed,
  squash merges, delete branch on merge.
- `main` protected (AD-19): required checks `gate` + `conventional-title`
  (strict), linear history, conversation resolution, no force push/
  deletion, admins included.
- Branch naming revised (AD-20, amends AD-19): `<type>/<issue>-<slug>`
  with the fixed Conventional Commits vocabulary; new `branch-name` CI
  check enforces format and prefix↔title consistency (issue #3, PR #4 —
  itself the first compliant branch).

## 2026-09-30 — Methodology layer landed; review-gate behavior mapped

- Issues #24–27 filed: adopt DDD practices, codify architecture
  constraints, enforce TDD/complexity budgets in CI, define the full
  testing stack. Survey pins from #12 become adopted practice there.
- #12 merged via #13 after a long review-gate
  marathon. Contents: superpowers path fixed (`plugins/superpowers/skills`
  → `skills`, the old pin 404'd), 15 methodology skill pins added with
  API path verification, `superpowers` declared as the one bundle entry
  (two-way declaration rule: the governing doc states the exception and
  the entry comment cites it).
- Review fixes with verified substance: Skills CLI `--skill` takes the
  upstream `SKILL.md` frontmatter name, not the lockfile path or alias
  (`tdd-practice` → upstream `test-driven-development`; `kubernetes` →
  `kubernetes-skill`); the CLI has no `--revision` flag — the immutable
  pin is encoded in the source ref as `github.com/<org>/<repo>#<sha>`.
- CodeRabbit behavior model, empirically mapped and recorded in #21:
  incremental reviews with zero findings are silent and mark the commit
  reviewed; `@coderabbitai full review` is the reliable verdict lever;
  clean first-pass PRs do not receive a formal APPROVED (every APPROVED
  in repo history ended a changes-requested → fix → re-review cycle);
  `required_conversation_resolution` blocks on outdated threads too;
  review dismissal works via GraphQL only (REST 404 with our token).
- Protection note: approving reviews require 1 reviewer with write
  access; the PR author cannot self-approve, so CodeRabbit's formal
  review is the only approval path on this solo-maintainer repo.
- auto-merge (squash, GraphQL-enabled) fired end-to-end once approval +
  resolution + checks aligned.

## 2026-10-01 — Go engineering methodology stack adopted

- Standing rule from maintainer: methodology questions are settled by
  mature upstream practice (research first, decide only when no good
  reference exists), never by blocking on a decision meeting. This
  milestone is the first execution of that rule.
- PR #36 (branch `feat/33-methodology-stack`, one commit per concern)
  landed AD-23, AD-24, AD-25 plus configs; closes #24–#27 (research
  issues) and #32–#35 (adoption issues). #32 fixed first: the ADR
  skeleton now emits the required `updated:` frontmatter field.
- AD-23: single root `go.mod`; per-component `app/domain/port/adapter`
  layers (wild-workouts hexagonal); domain stdlib-only; five independent
  v0 bounded contexts in `.go-arch-lint.yml` (gateway dp/cp, cicd,
  agentruntime, registry).
- AD-24: TDD red-green-refactor with tests in the same PR; complexity
  budgets funlen 50 / gocognit 30 / cyclop 15 as a ratchet that only
  tightens via superseding AD; nolintlint three locks; `.golangci.yml`
  v2 (standard + gosec + misspell).
- AD-25: unit (default) / `integration` (testcontainers-go; GitLab CE
  via `GenericContainer` — no upstream module) / `e2e` (deferred, tag
  defined now); regression as replay tests inside unit+integration;
  testify + go-cmp; 80% coverage via go-test-coverage; flake skips cite
  their `kind/flake` issue.
- Go CI jobs deliberately deferred per the CI-first rule: configs and
  `make lint-go` / `test-unit` / `test-coverage` (warn-and-skip without
  go.mod) land now, the enforcing jobs land with the first Go component.
- CodeRabbit caught one real defect in review: depguard `pkg` entries
  are package-path prefix matches, not regexes — the dotted-first-segment
  deny pattern never matched anything; fixed to the canonical
  `list-mode: strict` + `allow: [$gostd]`. Verified APPROVED →
  auto-merge fired without a manual nudge.
