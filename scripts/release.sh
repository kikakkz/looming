#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# release.sh — interactive per-component release driver (#166).
#
# Policy (release skill): components carry independent semver image
# tags, only the changed component is published per release, and a
# component release is followed by a PR bumping only that component's
# pin in bundle/manifest.yaml. Mechanics (release-l1 §3.2/§3.3): pushing
# <component>/vX.Y.Z triggers the release.yml component job, which builds
# and publishes that component's image only — this script only cuts and
# pushes the tag; the bundle release (bare vX.Y.Z) stays manual.
#
# Flow: discover the release names from bundle/manifest.yaml → pick a
# component (menu, or positional arg) → pick patch or minor — the
# version is derived from the component's existing tags by
# .ai/tools/release_version.py, never hand-typed → guard against
# existing tags → tag → push (retried) → print the workflow watch
# command and the follow-up steps (pin-bump PR, post-publish
# verification).
#
# Usage:
#   scripts/release.sh                       interactive menu (needs a TTY)
#   scripts/release.sh <component> <bump>    non-interactive (bump: patch|minor)
#   scripts/release.sh gateway patch --dry-run

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="$REPO_ROOT/bundle/manifest.yaml"
VERSION_TOOL="$REPO_ROOT/.ai/tools/release_version.py"

DRY_RUN=0
COMPONENT=""
BUMP=""
INTERACTIVE=0

log() { printf '==> %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
release.sh — interactive per-component release driver (#166).

Usage:
  scripts/release.sh                       interactive menu (needs a TTY)
  scripts/release.sh <component> <bump>    non-interactive (bump: patch|minor)
  scripts/release.sh gateway patch --dry-run

Components come from bundle/manifest.yaml (the release names); the
version is derived from the component's existing <component>/v* tags by
.ai/tools/release_version.py — patch bumps the third field, minor the
second (patch zeroed); a component with no tags starts at v0.1.0.
Pushing <component>/vX.Y.Z triggers release.yml's component job, which
builds and publishes that component's image only; this script only cuts
and pushes the tag. Positional args are the confirmation (no prompt);
--dry-run prints the plan and stops before tagging. After a successful
push, bump the component's pin in bundle/manifest.yaml (own PR).
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
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

if [[ -z "$COMPONENT" && -z "$BUMP" && ! -t 0 ]]; then
    usage >&2
    exit 2
fi
if [[ (-z "$COMPONENT" && -n "$BUMP") || (-n "$COMPONENT" && -z "$BUMP") ]]; then
    usage >&2
    exit 2
fi
if [[ -n "$BUMP" && "$BUMP" != "patch" && "$BUMP" != "minor" ]]; then
    usage >&2
    exit 2
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

if [[ -n "$COMPONENT" ]]; then
    known=0
    for c in "${COMPONENTS[@]}"; do
        [[ "$c" == "$COMPONENT" ]] && known=1 && break
    done
    [[ "$known" -eq 1 ]] || die \
        "'$COMPONENT' is not a release name in bundle/manifest.yaml: ${COMPONENTS[*]}"
else
    INTERACTIVE=1
fi

refresh_tags() {
    # Version math reads local tags; refresh them from origin first so
    # the menu reflects what CI and other maintainers already pushed.
    # An unreachable origin is a warning, not a stop — offline, local
    # tags still give an answer, and the remote tag guard fails closed.
    if git remote get-url origin >/dev/null 2>&1; then
        if ! GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git fetch --tags origin >/dev/null 2>&1; then
            warn "refreshing tags from origin failed — deriving versions from local tags only"
        fi
    fi
}

current_version() { # $1 = component; echoes vX.Y.Z, empty when unreleased
    local latest
    latest="$(git tag -l "$1/v*" | sort -V | tail -n 1)"
    [[ -n "$latest" ]] || return 0
    printf '%s\n' "${latest#"$1"/}"
}

next_version() { # $1 = component, $2 = bump; echoes the derived vX.Y.Z
    python3 "$VERSION_TOOL" --component "$1" --bump "$2"
}

choose_component() {
    local i reply c
    printf 'releasable components (from bundle/manifest.yaml):\n'
    for i in "${!COMPONENTS[@]}"; do
        local current
        current="$(current_version "${COMPONENTS[$i]}")"
        if [[ -n "$current" ]]; then
            printf '  %d) %s (current: %s)\n' "$((i + 1))" "${COMPONENTS[$i]}" "$current"
        else
            printf '  %d) %s (unreleased)\n' "$((i + 1))" "${COMPONENTS[$i]}"
        fi
    done
    while true; do
        read -r -p "select a component [1-${#COMPONENTS[@]}]: " reply \
            || die "no input — aborted, nothing was tagged or pushed"
        if [[ "$reply" =~ ^[0-9]+$ ]] \
            && ((reply >= 1 && reply <= ${#COMPONENTS[@]})); then
            COMPONENT="${COMPONENTS[$((reply - 1))]}"
            return 0
        fi
        for c in "${COMPONENTS[@]}"; do
            if [[ "$reply" == "$c" ]]; then
                COMPONENT="$c"
                return 0
            fi
        done
        warn "unrecognized selection: '$reply'"
    done
}

choose_bump() {
    local patch minor current
    patch="$(next_version "$COMPONENT" patch)"
    minor="$(next_version "$COMPONENT" minor)"
    current="$(current_version "$COMPONENT")"
    [[ -n "$current" ]] || current="none (unreleased)"
    cat <<EOF
selected: $COMPONENT (current: $current)
  1) patch -> $COMPONENT/$patch
  2) minor -> $COMPONENT/$minor
EOF
    local reply
    while true; do
        read -r -p "select a bump [1-2, patch, or minor]: " reply \
            || die "no input — aborted, nothing was tagged or pushed"
        case "$reply" in
            1 | patch) BUMP="patch"; return 0 ;;
            2 | minor) BUMP="minor"; return 0 ;;
            *) warn "unrecognized selection: '$reply'" ;;
        esac
    done
}

confirm() {
    local reply
    read -r -p "proceed? [y/N]: " reply || reply=""
    if [[ "$reply" != "y" && "$reply" != "Y" ]]; then
        log "aborted — nothing was tagged or pushed"
        exit 0
    fi
}

push_tag() { # $1 = tag; retry with backoff — a transient network blip
             # must not cost a re-run of the whole menu
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

refresh_tags
if [[ "$INTERACTIVE" -eq 1 ]]; then
    choose_component
    choose_bump
fi

CURRENT="$(current_version "$COMPONENT")"
VERSION="$(next_version "$COMPONENT" "$BUMP")"
TAG="$COMPONENT/$VERSION"

# Immutability guard (release-l1 §3.1(b)): refuse to touch an existing
# tag locally or on the remote. The remote check closes the
# "everything up to date" hole for a tag this clone has not fetched —
# without it the script would report success while publishing nothing.
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
    die "tag $TAG already exists locally — refusing to repoint it"
fi
if git remote get-url origin >/dev/null 2>&1; then
    if GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code --tags origin "$TAG" >/dev/null 2>&1; then
        die "tag $TAG already exists on origin — refusing to repoint it"
    fi
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

if [[ "$INTERACTIVE" -eq 1 ]]; then
    confirm
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
