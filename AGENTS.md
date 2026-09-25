# AGENTS.md

Looming is an enterprise agent-engineering platform shipped as a single
self-contained bundle. This file is the operating contract for every
contributor, human or AI. It is optimized for constraints and commands;
bulky knowledge lives in `docs/` and is referenced by pointer.

## Hard constraints (non-negotiable)

1. **Issue-driven development.** No issue, no code. Every PR references an
   issue (`Closes #N`), branches are named `<issue>-<slug>`, and the issue
   thread carries plan, status, and review artifacts.
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

- `make ci-gate` — run the full local gate (locks, tool tests, trailer
  checks, shell lint).
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
- `.ai/memory/decisions.md` — accepted architecture decisions (AD-1…).
  Read it before proposing anything architectural.

## Collaboration protocol

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
