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
  tests, trailer checks, ADR check, shell lint).
- `make check-branch` — validate the current branch name against the
  naming rule before push (same rule as the `branch-name` CI check;
  pass `--title "ci: ..."` to also check prefix/title consistency).
- `make validate-locks` — validate `.ai/*.lock.toml` files and the memory
  bank.
- `make test-tools` — unit tests for `.ai/tools/`.
- `make check-trailers` — validate commit-message trailers on `HEAD`.

## Directory map

- `cmd/` — component entrypoints (one directory per binary; empty until the
  gateway issue lands).
- `docs/` — long-form knowledge. Index: [docs/README.md](docs/README.md).
- `.ai/` — agent assets: skills, external skill pins, MCP server pins, repo
  tools, memory bank. Rules: [.ai/AGENTS.md](.ai/AGENTS.md).
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
