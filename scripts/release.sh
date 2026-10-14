#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# release.sh — per-component release driver (#166, AD-40 three-step flow
# since #172).
#
# Policy (release skill): components carry independent semver image
# tags, only the changed component is published per release, and a
# component release is followed by a PR bumping only that component's
# pin in bundle/manifest.yaml. Mechanics (release-l1 §3.1/§3.2, AD-40):
# the component-root VERSION file is the canonical version source and
# the git tag <component>/vX.Y.Z its mark — the release CI consistency
# gate refuses to publish when tag and file disagree. Releasing is three
# steps, and this script strings them together:
#   1. bump PR — write the next version into <component>/VERSION, commit
#      it on a chore/bump-<component>-<version> branch, push, and open
#      the PR ("chore: bump <component> VERSION to vX.Y.Z (#168)");
#   2. tag — once the PR is merged, re-running the same command detects
#      that origin/main carries the new version: tag <component>/<vX.Y.Z>
#      at origin/main and push the tag;
#   3. watch + follow up — print the workflow watch command and the
#      bundle pin-bump reminder.
# When the file's version carries no tag yet (pending first release),
# no bump is needed — the file already names the target and the flow
# starts directly at step 2.
#
# Two non-interactive surfaces (the interactive menu lives in the AI
# agent, not here — see the maintainer design note on #166):
#   --list  machine-readable candidate feed for the agent's native
#           menu: one TSV line per component, derived from its VERSION
#           file — never hand-typed;
#   run     scripts/release.sh <component> <patch|minor|major>
#           [--dry-run] walks the three steps above; positional args
#           are the confirmation (no prompt).
# Agent flow: --list → native menu (component + bump) → non-interactive
# run → report the workflow link and the follow-up steps.
#
# Usage:
#   scripts/release.sh --list
#   scripts/release.sh <component> <patch|minor|major> [--dry-run]
#   scripts/release.sh gateway patch --dry-run

set -euo pipefail

# Non-interactive credential lookup everywhere (non-interactive-credential-
# lookup): the terminal prompt and the askpass helper are both disabled,
# exported so no network git call below can re-enable them by omission.
export GIT_TERMINAL_PROMPT=0
export GIT_ASKPASS=

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
release.sh — per-component release driver (#166, AD-40 three-step flow).

Usage:
  scripts/release.sh --list                              machine-readable candidates
  scripts/release.sh <component> <bump> [--dry-run]      run the three-step release flow
  scripts/release.sh gateway patch --dry-run

Components come from bundle/manifest.yaml (the release names); versions
are derived from the component-root VERSION files by
.ai/tools/release_version.py (AD-40: the file is the canonical source,
the tag its mark) — patch bumps the third field, minor the second,
major the first (lower fields zeroed); a component with no VERSION file
starts at v0.1.0.

The release flow is three steps, all non-interactive:
  1. bump PR — write the next version into <component>/VERSION, commit
     on chore/bump-<component>-<version>, push, open the PR
     ("chore: bump <component> VERSION to vX.Y.Z (#168)");
  2. tag — after the PR merges, re-running the same command detects
     main carries the new version: tag <component>/<vX.Y.Z> at
     origin/main and push it; release.yml's component job then builds
     and publishes that component's image only;
  3. watch + follow up — gh run list --workflow=release.yml, then bump
     the component's pin in bundle/manifest.yaml (own PR).
When the file's version has no tag yet (pending first release), the
file already names the target — the flow starts directly at step 2.

--list prints one TSV line per component:
  component<TAB>current|none<TAB>patch candidate<TAB>minor candidate<TAB>major candidate
"current" is "none" for a component with no VERSION file yet (a stderr
note marks it unreleased). --list is the data source for AI agents'
native menus (list → menu → non-interactive run); this script has no
interactive prompts. --list and --dry-run are read-only:
no fetch, no ref updates. --dry-run prints the full three-step plan and
stops.

The release CI gate (AD-40) refuses to publish when the pushed tag and
the VERSION file disagree — land the bump first, tag second.
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

# Read the component's current version from its AD-40 VERSION file.
# Echoes vX.Y.Z; rc 3 with empty output when the file is absent (the
# component is unreleased); the tool's rc 1 propagates with its stderr
# message — bad content fails loud, never reading as "none".
read_current() { # $1 = component; $2 = explicit file path (optional)
    local f="${2:-$REPO_ROOT/$1/VERSION}"
    python3 "$VERSION_TOOL" --component "$1" --file "$f"
}

list_candidates() {
    local c current patch minor major rc
    for c in "${COMPONENTS[@]}"; do
        rc=0
        current="$(read_current "$c")" || rc=$?
        if [[ "$rc" -eq 3 ]]; then
            warn "$c: $c/VERSION missing — treating as unreleased (initial release v0.1.0)"
            current="none"
        elif [[ "$rc" -ne 0 ]]; then
            exit "$rc"
        fi
        patch="$(python3 "$VERSION_TOOL" --component "$c" --bump patch --file "$REPO_ROOT/$c/VERSION")"
        minor="$(python3 "$VERSION_TOOL" --component "$c" --bump minor --file "$REPO_ROOT/$c/VERSION")"
        major="$(python3 "$VERSION_TOOL" --component "$c" --bump major --file "$REPO_ROOT/$c/VERSION")"
        printf '%s\t%s\t%s\t%s\t%s\n' "$c" "$current" "$patch" "$minor" "$major"
    done
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

VERSION_REL="$COMPONENT/VERSION"
VERSION_ABS="$REPO_ROOT/$VERSION_REL"

rc=0
CURRENT="$(read_current "$COMPONENT")" || rc=$?
if [[ "$rc" -eq 3 ]]; then
    CURRENT=""
elif [[ "$rc" -ne 0 ]]; then
    exit "$rc"
fi

# Is the file's current version already tagged (locally, else on
# origin)? Only a released current version gets bumped; an untagged one
# IS the pending release. ls-remote exit 2 is the only "no such tag":
# any other non-zero means the check itself failed (network/auth), which
# must fail closed — proceeding blind could misjudge the flow's phase.
RELEASED=0
if [[ -n "$CURRENT" ]]; then
    if git rev-parse -q --verify "refs/tags/$COMPONENT/$CURRENT" >/dev/null; then
        RELEASED=1
    elif git remote get-url origin >/dev/null 2>&1; then
        ls_remote_rc=0
        GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code --tags origin "$COMPONENT/$CURRENT" >/dev/null 2>&1 \
            || ls_remote_rc=$?
        case "$ls_remote_rc" in
            0) RELEASED=1 ;;
            2) ;;
            *) die "could not check $COMPONENT/$CURRENT on origin (tag lookup exit $ls_remote_rc) — failing closed; resolve the network or auth issue and re-run" ;;
        esac
    fi
fi

# The version this run releases.
if [[ -z "$CURRENT" ]]; then
    TARGET="v0.1.0"
    VERSION_NOTE="initial release — step 1 creates $VERSION_REL"
elif [[ "$RELEASED" -eq 1 ]]; then
    TARGET="$(python3 "$VERSION_TOOL" --component "$COMPONENT" --bump "$BUMP" --file "$VERSION_ABS")"
    VERSION_NOTE="$BUMP bump from $CURRENT"
else
    TARGET="$CURRENT"
    VERSION_NOTE="pending release — $VERSION_REL already names $CURRENT (no bump needed)"
fi
TAG="$COMPONENT/$TARGET"
BRANCH_NAME="chore/bump-$COMPONENT-$TARGET"

# Immutability guard (release-l1 §3.1(b)): refuse to touch an existing
# tag locally or on the remote. The remote check closes the
# "everything up to date" hole for a tag this clone has not seen —
# without it the script would report success while publishing nothing.
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

pr_title() {
    printf 'chore: bump %s VERSION to %s (#168)\n' "$COMPONENT" "$TARGET"
}

pr_body() {
    cat <<EOF
Step 1 of the AD-40 three-step component release flow (#168): bump the
component-root VERSION file — the canonical version source — to $TARGET.

The release CI consistency gate enforces tag == VERSION (release-l1
§3.1(c)), so the tag lands only after this file reaches main. After
merge, re-run the same command to continue:

    scripts/release.sh $COMPONENT $BUMP

Step 2 then tags $TAG at origin/main and pushes it; step 3 prints the
publish watch command and the bundle pin-bump reminder.
EOF
}

print_step3() {
    cat <<EOF

step 3 — watch the publish and follow up (release skill):
  1. Watch: gh run list --workflow=release.yml
  2. After green, bump the $COMPONENT pin in bundle/manifest.yaml — own
     PR, only that pin.
  3. Post-publish verification: the job fails closed while the new ghcr
     package is private. Flip visibility on the package's settings page,
     then re-run verification only:
       gh workflow run release.yml -f component=$COMPONENT -f version=$TARGET -f verify_only=true
  4. Bundle releases (bare vX.Y.Z tag) stay manual — outside this driver.
EOF
}

print_plan() { # $1 = phase note
    local step1_line
    if [[ -z "$CURRENT" ]]; then
        step1_line="create $VERSION_REL naming $TARGET (initial release) on branch $BRANCH_NAME, commit, push, open PR \"$(pr_title)\""
    elif [[ "$CURRENT" == "$TARGET" ]]; then
        if [[ "$RELEASED" -eq 1 ]]; then
            step1_line="bump already applied in this worktree — commit/push/PR on $BRANCH_NAME, then merge and re-run"
        else
            step1_line="no bump needed — $VERSION_REL already names $TARGET; step 1 is satisfied once the file is on main"
        fi
    else
        step1_line="$VERSION_REL: $CURRENT -> $TARGET on branch $BRANCH_NAME, commit, push, open PR \"$(pr_title)\""
    fi
    cat <<EOF
release plan (AD-40 three-step flow, #168):
  component:  $COMPONENT
  version:    $TARGET ($VERSION_NOTE)
  tag:        $TAG

  step 1 — bump VERSION:
              $step1_line
  step 2 — tag and push:
              once origin/main carries $TARGET, re-running detects it:
              tag $TAG at origin/main and push — release.yml builds and
              publishes ghcr.io/kikakkz/looming/$COMPONENT:$TARGET (this
              component only)
  step 3 — watch and follow up:
              gh run list --workflow=release.yml
              then bump the $COMPONENT pin in bundle/manifest.yaml (own PR)

  phase:      $1
EOF
}

if [[ "$DRY_RUN" -eq 1 ]]; then
    if [[ -z "$CURRENT" ]]; then
        phase="step 1 pending — $VERSION_REL absent here"
    elif [[ "$CURRENT" == "$TARGET" && "$RELEASED" -eq 1 ]]; then
        phase="step 1 applied in this worktree — awaiting merge on origin/main"
    elif [[ "$CURRENT" == "$TARGET" ]]; then
        phase="step 1 satisfied — pending release, awaiting the tag"
    else
        phase="step 1 pending"
    fi
    print_plan "$phase"
    log "dry-run — plan only; nothing written, no tag created, nothing pushed"
    exit 0
fi

git remote get-url origin >/dev/null 2>&1 \
    || die "the release flow needs origin (push + merge detection); --list and --dry-run work without it"
command -v gh >/dev/null 2>&1 \
    || warn "gh CLI not found — step 1's PR creation will fail; install gh first"

# The VERSION content of $COMPONENT at a ref — the merge detection that
# drives step 2. Empty output when the file is absent there; the tool's
# rc 1 (bad content) propagates with its stderr message.
version_at_ref() { # $1 = ref
    local content version rc
    content="$(git show "$1:$VERSION_REL" 2>/dev/null)" || return 0
    rc=0
    version="$(printf '%s\n' "$content" | python3 "$VERSION_TOOL" --component "$COMPONENT" --file -)" || rc=$?
    case "$rc" in
        0) printf '%s\n' "$version" ;;
        3) return 0 ;;
        *) return "$rc" ;;
    esac
}

push_ref() { # $1 = ref (tag or branch); retry with backoff — a transient
             # network blip must not cost a re-run of the whole flow
    local ref="$1" attempt
    for attempt in 1 2 3 4 5; do
        if GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git push origin "$ref"; then
            return 0
        fi
        warn "push attempt $attempt/5 failed — retrying"
        sleep $((attempt * 2))
    done
    die "pushing $ref failed after 5 attempts — the local ref remains; resolve the network issue, then push $ref to origin manually"
}

PREV_BRANCH=""
restore_branch() {
    if [[ -n "$PREV_BRANCH" ]]; then
        git checkout -q "$PREV_BRANCH" 2>/dev/null || true
    fi
}
trap restore_branch EXIT

write_version_file() {
    cat > "$VERSION_ABS" <<EOF
# SPDX-License-Identifier: Apache-2.0

# Canonical component version (AD-40): this file is the single source of
# truth for $COMPONENT's version — the git tag $COMPONENT/vX.Y.Z and the
# image tag mark it and must equal this content exactly. The release CI
# consistency gate refuses to publish on mismatch (release-l1 §3.1(c)).
$TARGET
EOF
}

open_pr() { # $1 = branch, $2 = title
    local url
    # --repo pins every repo-scoped call to this repository (gh-access-
    # check-must-be-host-pinned): GH_HOST/GH_REPO overrides must not
    # redirect the PR check or creation anywhere else.
    if ! gh pr view --repo kikakkz/looming "$1" >/dev/null 2>&1; then
        url="$(gh pr create --repo kikakkz/looming --base main --head "$1" --title "$2" --body "$(pr_body)")" \
            || die "gh pr create failed — the bump branch $1 is pushed; open the PR manually, then re-run after merge"
        log "step 1 — PR opened: $url"
    else
        log "step 1 — PR for $1 already exists — merge it, then re-run: scripts/release.sh $COMPONENT $BUMP"
    fi
}

step1_bump_pr() { # $1 = 1 when the worktree already carries TARGET
    local msg_title
    msg_title="$(pr_title)"
    PREV_BRANCH="$(git branch --show-current)"
    [[ -n "$PREV_BRANCH" ]] \
        || die "detached HEAD — checkout a branch before running the release flow"
    # Every branch-selection path below validates against origin/main,
    # so fetch it up front (not only when creating the branch).
    GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git fetch -q origin main \
        || die "git fetch origin main failed — resolve the network/auth issue and re-run"
    if [[ "$1" -eq 1 && "$(git branch --show-current)" == "$BRANCH_NAME" ]]; then
        :
    elif git show-ref --verify --quiet "refs/heads/$BRANCH_NAME"; then
        log "step 1 — reusing existing bump branch $BRANCH_NAME"
        git checkout -q "$BRANCH_NAME"
    else
        log "step 1 — creating bump branch $BRANCH_NAME from origin/main"
        git checkout -q -b "$BRANCH_NAME" origin/main
    fi
    # CodeRabbit #176: a reused bump branch must still sit on origin/main
    # and carry nothing outside the VERSION file — a stale or
    # contaminated branch would smuggle unrelated changes into the PR.
    git merge-base --is-ancestor origin/main "$BRANCH_NAME" \
        || die "bump branch $BRANCH_NAME is not based on the current origin/main — remove or rebase it before re-running"
    [[ -z "$(git diff --name-only "origin/main...$BRANCH_NAME" -- . ":(exclude)$VERSION_REL")" ]] \
        || die "bump branch $BRANCH_NAME holds changes outside $VERSION_REL — remove or recreate it before re-running"
    write_version_file
    git add -- "$VERSION_REL"
    # --only keeps other staged changes out of the bump commit.
    if git commit -q -m "$msg_title" --only "$VERSION_REL"; then
        log "step 1 — committed $(git rev-parse --short HEAD): $msg_title"
    else
        log "step 1 — $VERSION_REL already carries $TARGET (nothing to commit)"
    fi
    push_ref "$BRANCH_NAME"
    open_pr "$BRANCH_NAME" "$msg_title"
    log "step 1 done — merge the PR, then re-run to continue: scripts/release.sh $COMPONENT $BUMP"
}

step2_tag_push() { # FETCH_HEAD holds the fetched origin/main
    local sha
    sha="$(git rev-parse -q --verify FETCH_HEAD)" \
        || die "FETCH_HEAD unresolved after fetching origin main — cannot tag"
    log "step 2 — origin/main carries $VERSION_REL $TARGET at $sha"
    git tag "$TAG" "$sha"
    log "step 2 — tagged $TAG at $sha"
    push_ref "$TAG"
    log "step 2 done — pushed $TAG; release.yml's component job publishes ghcr.io/kikakkz/looming/$COMPONENT:$TARGET (this component only)"
    print_step3
}

log "fetching origin/main to detect the flow's phase"
GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git fetch -q origin main \
    || die "git fetch origin main failed — resolve the network/auth issue and re-run"
MAIN_CURRENT="$(version_at_ref FETCH_HEAD)"

if [[ "$MAIN_CURRENT" == "$TARGET" ]]; then
    step2_tag_push
elif [[ "$CURRENT" == "$TARGET" ]]; then
    log "step 1 — the bump is already in this worktree but not on origin/main yet"
    step1_bump_pr 1
else
    step1_bump_pr 0
fi
