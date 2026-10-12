#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# release.sh — per-component release driver (#166).
#
# Policy (release skill): components carry independent semver image
# tags, only the changed component is published per release, and a
# component release is followed by a PR bumping only that component's
# pin in bundle/manifest.yaml. Mechanics (release-l1 §3.2/§3.3): pushing
# <component>/vX.Y.Z triggers the release.yml component job, which builds
# and publishes that component's image only — this script only cuts and
# pushes the tag; the bundle release (bare vX.Y.Z) stays manual.
#
# Two non-interactive surfaces (the interactive menu lives in the AI
# agent, not here — see the maintainer design note on #166):
#   --list  machine-readable candidate feed for the agent's native
#           menu: one TSV line per component, derived from its existing
#           tags — never hand-typed;
#   run     scripts/release.sh <component> <patch|minor|major>
#           [--dry-run] cuts and pushes the tag; positional args are
#           the confirmation (no prompt).
# Agent flow: --list → native menu (component + bump) → non-interactive
# run → report the workflow link and the follow-up steps.
#
# Usage:
#   scripts/release.sh --list
#   scripts/release.sh <component> <patch|minor|major> [--dry-run]
#   scripts/release.sh gateway patch --dry-run

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="$REPO_ROOT/bundle/manifest.yaml"
VERSION_TOOL="$REPO_ROOT/.ai/tools/release_version.py"

LIST=0
DRY_RUN=0
COMPONENT=""
BUMP=""

log() { printf '==> %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
release.sh — per-component release driver (#166).

Usage:
  scripts/release.sh --list                              machine-readable candidates
  scripts/release.sh <component> <bump> [--dry-run]      cut and push the release tag
  scripts/release.sh gateway patch --dry-run

Components come from bundle/manifest.yaml (the release names); versions
are derived from the component's existing <component>/v* tags by
.ai/tools/release_version.py — patch bumps the third field, minor the
second, major the first (lower fields zeroed); a component with no tags
starts at v0.1.0. Pushing <component>/vX.Y.Z triggers release.yml's
component job, which builds and publishes that component's image only;
this script only cuts and pushes the tag.

--list prints one TSV line per component:
  component<TAB>current|none<TAB>patch candidate<TAB>minor candidate<TAB>major candidate
"current" is "none" for a component with no tags yet. --list is the
data source for AI agents' native menus (list → menu → non-interactive
run); this script has no interactive prompts. It is read-only apart
from the best-effort tag refresh (a failed refresh degrades to locally
known tags with a stderr note). --dry-run prints the plan and stops
before tagging. After a successful push, bump the component's pin in
bundle/manifest.yaml (own PR).
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --list) LIST=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        -h | --help) usage; exit 0 ;;
        *)
            if [[ -z "$COMPONENT" ]]; then
                COMPONENT="$1"
            elif [[ -z "$BUMP" ]]; then
                BUMP="$1"
            else
                die "unknown argument: $1 (see --help)"
            fi
            shift
            ;;
    esac
done

if [[ "$LIST" -eq 1 ]]; then
    if [[ -n "$COMPONENT" || -n "$BUMP" || "$DRY_RUN" -eq 1 ]]; then
        usage >&2
        exit 2
    fi
elif [[ -z "$COMPONENT" && -z "$BUMP" ]]; then
    usage >&2
    exit 2
elif [[ (-z "$COMPONENT" && -n "$BUMP") || (-n "$COMPONENT" && -z "$BUMP") ]]; then
    usage >&2
    exit 2
elif [[ -n "$BUMP" ]]; then
    case "$BUMP" in
        patch | minor | major) ;;
        *)
            usage >&2
            exit 2
            ;;
    esac
fi

cd "$REPO_ROOT"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 \
    || die "not inside a git repository ($REPO_ROOT)"

# The release names are the manifest keys — the same names release.yml's
# tag patterns and allowlist gate on (release-l1 §3.2).
components() {
    awk '
        /^components:/ { inc = 1; next }
        inc && /^[^[:space:]]/ { inc = 0 }
        inc && /^  [a-z]+: v[0-9]+\.[0-9]+\.[0-9]+$/ {
            name = $0
            sub(/^  /, "", name)
            sub(/:.*/, "", name)
            print name
        }
    ' "$MANIFEST"
}

mapfile -t COMPONENTS < <(components)
[[ ${#COMPONENTS[@]} -gt 0 ]] \
    || die "bundle/manifest.yaml pins no components — nothing to release"

refresh_tags() {
    # Version math reads local tags; refresh them from origin first so
    # the candidates reflect what CI and other maintainers already
    # pushed. An unreachable origin is a warning, not a stop — offline,
    # local tags still give an answer, and the remote tag guard fails
    # closed.
    #
    # Never called on the execution path's --dry-run: a fetch writes
    # remote tags into local refs, which a dry run must not do (review
    # thread on PR #167) — there the plan derives from local tags only.
    if git remote get-url origin >/dev/null 2>&1; then
        if ! GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git fetch --tags origin >/dev/null 2>&1; then
            warn "refreshing tags from origin failed — deriving versions from local tags only"
        fi
    fi
}

current_version() { # $1 = component; echoes vX.Y.Z, empty when unreleased
    # The same strict shape release_version.py parses: the glob also
    # matches pre-release/malformed tags (v1.0.0-rc.1), which must not
    # define the displayed base version.
    local latest
    latest="$(git tag -l "$1/v*" \
        | grep -E "^$1/v[0-9]+\.[0-9]+\.[0-9]+$" \
        | sort -V | tail -n 1 || true)"
    [[ -n "$latest" ]] || return 0
    printf '%s\n' "${latest#"$1"/}"
}

next_version() { # $1 = component, $2 = bump; echoes the derived vX.Y.Z
    python3 "$VERSION_TOOL" --component "$1" --bump "$2"
}

list_candidates() {
    refresh_tags
    local c current patch minor major
    for c in "${COMPONENTS[@]}"; do
        current="$(current_version "$c")"
        [[ -n "$current" ]] || current="none"
        patch="$(next_version "$c" patch)"
        minor="$(next_version "$c" minor)"
        major="$(next_version "$c" major)"
        printf '%s\t%s\t%s\t%s\t%s\n' "$c" "$current" "$patch" "$minor" "$major"
    done
}

push_tag() { # $1 = tag; retry with backoff — a transient network blip
             # must not cost a re-run of the whole flow
    local tag="$1" attempt
    for attempt in 1 2 3 4 5; do
        if GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git push origin "$tag"; then
            return 0
        fi
        warn "push attempt $attempt/5 failed — retrying"
        sleep $((attempt * 2))
    done
    die "pushing $tag failed after 5 attempts — the local tag remains; resolve the network issue, then push $tag to origin manually"
}

if [[ "$LIST" -eq 1 ]]; then
    list_candidates
    exit 0
fi

known=0
for c in "${COMPONENTS[@]}"; do
    [[ "$c" == "$COMPONENT" ]] && known=1 && break
done
[[ "$known" -eq 1 ]] || die \
    "'$COMPONENT' is not a release name in bundle/manifest.yaml: ${COMPONENTS[*]}"

# A dry run must not write refs: skip the tag refresh (see refresh_tags)
# and derive the plan from locally known tags.
if [[ "$DRY_RUN" -eq 0 ]]; then
    refresh_tags
fi

CURRENT="$(current_version "$COMPONENT")"
VERSION="$(next_version "$COMPONENT" "$BUMP")"
TAG="$COMPONENT/$VERSION"

# Immutability guard (release-l1 §3.1(b)): refuse to touch an existing
# tag locally or on the remote. The remote check closes the
# "everything up to date" hole for a tag this clone has not fetched —
# without it the script would report success while publishing nothing.
# ls-remote exit 2 is the only "no such tag": any other non-zero means
# the check itself failed (network/auth), which must fail closed —
# proceeding blind could overwrite an immutable published tag.
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
    die "tag $TAG already exists locally — refusing to repoint it"
fi
if git remote get-url origin >/dev/null 2>&1; then
    ls_remote_rc=0
    GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code --tags origin "$TAG" >/dev/null 2>&1 \
        || ls_remote_rc=$?
    case "$ls_remote_rc" in
        0) die "tag $TAG already exists on origin — refusing to repoint it" ;;
        2) ;;
        *) die "could not check $TAG on origin (tag lookup exit $ls_remote_rc) — failing closed; resolve the network or auth issue and re-run" ;;
    esac
fi

FROM="$CURRENT"
[[ -n "$FROM" ]] || FROM="none (initial release)"
cat <<EOF
release plan:
  component:  $COMPONENT
  version:    $VERSION ($BUMP bump from $FROM)
  tag:        $TAG at HEAD $(git rev-parse --short HEAD)
  publish:    $TAG -> origin; release.yml builds and publishes
              ghcr.io/kikakkz/looming/$COMPONENT:$VERSION (this component only)
  watch:      gh run list --workflow=release.yml
EOF

if [[ "$DRY_RUN" -eq 1 ]]; then
    log "dry-run — no tag created, nothing pushed"
    exit 0
fi

git tag "$TAG"
log "tagged $TAG"
push_tag "$TAG"

log "pushed $TAG — release.yml's component job publishes ghcr.io/kikakkz/looming/$COMPONENT:$VERSION (this component only)"
cat <<EOF

Next steps (release skill):
  1. Bump the $COMPONENT pin in bundle/manifest.yaml — own PR, only
     that pin.
  2. Post-publish verification: the job fails closed while the new ghcr
     package is private. Flip visibility on the package's settings page,
     then re-run verification only:
       gh workflow run release.yml -f component=$COMPONENT -f version=$VERSION -f verify_only=true
  3. Bundle releases (bare vX.Y.Z tag) stay manual — outside this menu.
EOF
