---
updated: 2026-09-25
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
