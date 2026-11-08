#!/usr/bin/env bash
# check-trailers.sh — validate commit-message attribution policy.
#
# Policy (see .github/CONTRIBUTING.md and AGENTS.md):
#   - Co-Authored-By is banned (it misattributes authorship and breaks DCO).
#   - AI assistance is disclosed with Assisted-by: / Generated-by: trailers.
#   - AI-authored commits (Generated-by:) carry the maintainer's standing
#     DCO sign-off, pinned to the exact signer: Signed-off-by is required
#     on them with or without --dco (#194).
#   - With --dco, every commit must carry Signed-off-by (human DCO).
#
# Usage: check-trailers.sh [--dco] [REV_RANGE]
#   REV_RANGE defaults to the branch's own commits: the first resolvable
#   of origin/main/main is the baseline (base..HEAD, same scope as CI's
#   --dco job). Whole-history checks stay available by passing HEAD
#   explicitly. Pre-policy history is never in the default scope, so new
#   rules do not retroactively fail the gate.
#
# Iterates commit-by-commit (not line-by-line): each commit's full body is
# one unit, so multi-line bodies are checked once and reports name the
# offending commit.

set -euo pipefail

require_dco=0
rev_range=""
range_given=0

for arg in "$@"; do
    case "$arg" in
        --dco) require_dco=1 ;;
        *) rev_range="$arg"; range_given=1 ;;
    esac
done

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "ERROR: not inside a git repository." >&2
    exit 1
fi

if [ "$range_given" -eq 0 ]; then
    for base in origin/main main; do
        if git rev-parse --verify -q "$base" >/dev/null 2>&1; then
            rev_range="$base..HEAD"
            break
        fi
    done
    if [ -z "$rev_range" ]; then
        echo "no baseline ref (origin/main, main) resolvable; " \
             "skipping trailer check"
        exit 0
    fi
fi

if ! git rev-list --max-count=1 "$rev_range" >/dev/null 2>&1; then
    if [ "$(git rev-list --all --count 2>/dev/null || echo 0)" -eq 0 ]; then
        echo "repository has no commits yet; nothing to check"
        exit 0
    fi
    echo "ERROR: cannot resolve revision range '$rev_range'." >&2
    exit 1
fi

failures=0
mapfile -t commits < <(git rev-list "$rev_range")
echo "checking range: $rev_range (${#commits[@]} commit(s))"

for sha in "${commits[@]}"; do
    body=$(git log -1 --format='%B' "$sha")

    if printf '%s\n' "$body" | grep -qiE '^Co-Authored-By:'; then
        echo "ERROR [$sha]: Co-Authored-By is banned (use Assisted-by:/Generated-by: for AI)." >&2
        failures=1
    fi

    if printf '%s\n' "$body" | grep -qiE '^(assisted|generated)[ -]?by:' \
        && ! printf '%s\n' "$body" | grep -qE '^(Assisted-by|Generated-by): .+'; then
        echo "ERROR [$sha]: malformed AI trailer (must be 'Assisted-by: <what>' or 'Generated-by: <what>')." >&2
        failures=1
    fi

    if printf '%s\n' "$body" | grep -qE '^Generated-by: .+' \
        && ! printf '%s\n' "$body" | grep -qE '^Signed-off-by: Zhao KK <kikakkz@hotmail\.com>$'; then
        echo "ERROR [$sha]: AI-authored commit (Generated-by:) must carry the maintainer's Signed-off-by (#194)." >&2
        failures=1
    fi

    if [ "$require_dco" -eq 1 ] && ! printf '%s\n' "$body" | grep -qiE '^Signed-off-by: .+'; then
        echo "ERROR [$sha]: missing Signed-off-by (DCO). Commit with: git commit -s" >&2
        failures=1
    fi
done

if [ "$failures" -ne 0 ]; then
    echo "trailer check FAILED" >&2
    exit 1
fi
echo "trailer check: OK"
