---
name: gh-setup
description: Ensure the GitHub CLI (gh) is installed and authenticated for this repository. Use when gh is missing or unauthenticated, and before any workflow that drives PRs or issues (review loops, pr-watch, release). Linux only.
---

# gh setup

Makes `gh` usable for this repository: installs a pinned release
user-locally (no root) and authenticates from the git credential store.
Idempotent — a machine that is already set up is left untouched. The
implementation lives in the reviewed, tested repository tool
[setup_gh.sh](../../tools/setup_gh.sh); this skill is only the workflow.

## Run

```bash
bash .ai/tools/setup_gh.sh
```

Exits 0 and prints `gh configured` (or `gh ready` when nothing was
needed). Individual steps: `check`, `install`, `auth`, `verify`. On
non-Linux systems install `gh` with the OS package manager
([cli.github.com](https://cli.github.com/)) and run
`bash .ai/tools/setup_gh.sh auth`.

## What it does, and the invariants it keeps

- `check` requires gh >= 2.53.0 (probes `--active` support), the active
  `github.com` account to authenticate, and read access to this
  repository. A pre-existing `hosts.yml` is tightened to mode 0600, and
  a chmod failure aborts instead of reporting readiness.
- `install` fetches the pinned tarball, verifies it against the pinned
  sha256 sums (aborting before extraction on mismatch), extracts beside
  the target and swaps only on success, then symlinks into
  `~/.local/bin` (must precede any older gh on PATH — the script checks
  and fails otherwise).
- `auth` reads the `github.com` credential via `git credential fill`,
  writes gh's own `hosts.yml` (gh's config-dir resolution:
  `GH_CONFIG_DIR`, then `XDG_CONFIG_HOME/gh`, then `~/.config/gh`) with
  mode 0600, tightening any existing file first. An existing file is
  kept under a unique, non-overwriting backup name for manual recovery,
  never silently overwritten; then
  `gh auth setup-git --hostname github.com` must succeed.
- `verify` re-runs the auth and repository-read checks.

Tokens never appear on a command line or in output, `gh auth status`
output is never printed (gh up to 2.96.0 can expose token prefixes),
and xtrace is disabled around credential handling. Only the invoking
user's environment is touched; no sudo.

## Bumping the gh version

Update `GH_VERSION` and both pinned sums (taken from the release's
`gh_<version>_checksums.txt`) in `setup_gh.sh`, in one PR. CI runs the
tool's unit tests and shellcheck on every change.
