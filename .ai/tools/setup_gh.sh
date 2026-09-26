#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# setup_gh.sh — install and configure the GitHub CLI (gh) for this repository.
#
# Idempotent: exits 0 when gh is already installed and authenticated. When
# it is not, installs a pinned release user-locally (no root) and
# authenticates from the git credential store. Tokens and `gh auth status`
# output are never printed.
#
# Usage: setup_gh.sh [check|install|auth|verify|all]
#   check    gate: gh >= 2.53.0, github.com auth (active account), repo read
#   install  pinned tarball into ~/.local/opt, symlinked into ~/.local/bin
#   auth     bootstrap gh config from the git credential store
#   verify   auth + repository-read check
#   all      check -> install (when needed) -> auth (when needed) -> verify
#
# Linux only. Bump GH_VERSION and both pinned sums (from the release's
# gh_<version>_checksums.txt) together, in one PR.

set -euo pipefail

GH_VERSION=2.101.0
SUM_AMD64=9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8
SUM_ARM64=b57e8063f18862647c9d22727c32e9da1b963f8bf9db648fe123a6975695640f

log() { printf '%s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

config_dir() {
    # gh's own resolution order: GH_CONFIG_DIR, then XDG_CONFIG_HOME/gh,
    # then ~/.config/gh. See `gh help environment`.
    if [ -n "${GH_CONFIG_DIR:-}" ]; then
        printf '%s\n' "$GH_CONFIG_DIR"
    else
        printf '%s\n' "${XDG_CONFIG_HOME:-$HOME/.config}/gh"
    fi
}

gh_ready() {
    local slug
    command -v gh >/dev/null 2>&1 || return 1
    gh auth status --help 2>/dev/null | grep -q -- --active || return 1
    gh auth status --hostname github.com --active >/dev/null 2>&1 || return 1
    slug=$(repo_slug) || return 1
    gh pr list --limit 1 --repo "$slug" >/dev/null 2>&1 || return 1
}

repo_slug() {
    # this repository, resolved from the checkout and host-pinned to
    # github.com — GH_REPO and GH_HOST must not be able to redirect the
    # access check elsewhere
    local url
    # --local and cleared repo-selection env: a GIT_DIR supplied by a
    # hook, or a global remote.origin.url fallback, must not redirect
    # the access check to another repository
    url=$(env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE \
        git config --local --get remote.origin.url 2>/dev/null) || return 1
    url=${url%.git}
    case "$url" in
        git@github.com:*/*) printf 'github.com/%s\n' "${url#git@github.com:}" ;;
        ssh://git@github.com/*/*) printf 'github.com/%s\n' "${url#ssh://git@github.com/}" ;;
        https://github.com/*/*) printf 'github.com/%s\n' "${url#https://github.com/}" ;;
        http://github.com/*/*) printf 'github.com/%s\n' "${url#http://github.com/}" ;;
        *) return 1 ;;
    esac
}

secure_hosts_yml() {
    local f
    f="$(config_dir)/hosts.yml"
    [ -f "$f" ] || return 0
    chmod 600 "$f" 2>/dev/null || die "cannot tighten permissions on $f (fail closed)"
}

cmd_check() {
    if gh_ready; then
        secure_hosts_yml
        log "gh ready"
        return 0
    fi
    return 1
}

cmd_install() {
    local sys arch sum name url tmp tmpd target selected
    sys=$(uname -s)
    [ "$sys" = Linux ] || die "installs Linux binaries only; on $sys use the OS package manager (e.g. 'brew install gh')"
    case $(uname -m) in
        x86_64) arch=amd64; sum=$SUM_AMD64 ;;
        aarch64) arch=arm64; sum=$SUM_ARM64 ;;
        *) die "unsupported architecture: $(uname -m)" ;;
    esac
    name="gh_${GH_VERSION}_linux_${arch}"
    url="https://github.com/cli/cli/releases/download/v${GH_VERSION}/${name}.tar.gz"
    target="$HOME/.local/opt/$name"

    mkdir -p "$HOME/.local/opt" "$HOME/.local/bin"
    # the checksum and private staging do not protect the final paths:
    # a permissive umask or a pre-existing writable directory would let
    # another local user swap the installed binary or symlink
    ensure_private_dir "$HOME/.local/opt" \
        || die "$HOME/.local/opt cannot be made user-private; refusing to install"
    ensure_private_dir "$HOME/.local/bin" \
        || die "$HOME/.local/bin cannot be made user-private; refusing to install"
    tmp=$(mktemp "/tmp/${name}.tar.gz.XXXXXX")
    curl -fL --retry 3 --max-time 300 --retry-max-time 900 -o "$tmp" "$url" || { rm -f "$tmp"; die "download failed: $url"; }
    # a failed check must abort before anything is extracted (CWE-494)
    echo "$sum  $tmp" | sha256sum -c - >/dev/null \
        || { rm -f "$tmp"; die "checksum mismatch for $name"; }
    # stage on the same filesystem as the target so the final mv is a
    # rename, not a copy: a failed run must never remove the previous
    # installation
    tmpd=$(mktemp -d "$HOME/.local/opt/${name}.stage.XXXXXX")
    tar -xzf "$tmp" -C "$tmpd" || { rm -rf "$tmp" "$tmpd"; die "extract failed"; }
    rm -f "$tmp"
    [ -x "$tmpd/$name/bin/gh" ] \
        || { rm -rf "$tmpd"; die "archive layout unexpected: $name/bin/gh missing"; }
    # keep the previous installation until post-install verification
    # has passed: stage it aside in a unique directory (mv -T so an
    # existing path cannot silently become the parent), roll back on
    # any failure, remove the backup only after the checks below
    fail_install() {
        rm -rf "$tmpd"
        if [ -n "$prevdir" ] && [ -e "$prevdir/$name" ]; then
            rm -rf "$target"
            mv -T "$prevdir/$name" "$target" 2>/dev/null || true
            rm -rf "$prevdir"
        fi
        die "$1"
    }
    prevdir=
    if [ -e "$target" ]; then
        prevdir=$(mktemp -d "$HOME/.local/opt/${name}.prev.XXXXXX") \
            || { rm -rf "$tmpd"; die "cannot reserve a backup path; installation untouched"; }
        if ! mv -T "$target" "$prevdir/$name"; then
            rm -rf "$prevdir" "$tmpd"
            die "cannot stage previous $target aside; installation untouched"
        fi
    fi
    if ! mv "$tmpd/$name" "$target"; then
        fail_install "install failed; previous installation restored"
    fi
    rmdir "$tmpd" 2>/dev/null || true
    ln -sfn "$target/bin/gh" "$HOME/.local/bin/gh"
    hash -r 2>/dev/null || true
    # the binary selected from PATH must be the one just installed
    selected=$(command -v gh) \
        || fail_install "installed, but ~/.local/bin is not on PATH"
    [ "$(realpath "$selected")" = "$(realpath "$HOME/.local/bin/gh")" ] \
        || fail_install "another gh shadows $HOME/.local/bin/gh (selected: $selected); fix PATH order"
    if [ -n "$prevdir" ]; then
        rm -rf "$prevdir"
    fi
}

ensure_private_dir() {
    # Make $1 and its existing parents safe for credential staging.
    # Components we own are tightened to 0700 when a permissive umask
    # left them group/other-writable; components owned by others are
    # accepted only with the sticky bit (this is how /tmp, owned by
    # root, stays safe). Returns 1 when the path cannot be made safe.
    case $1 in
        /*) ;;
        *) return 1 ;; # relative paths would loop on ${d%/*}
    esac
    local d p m o
    d=$1
    while [ -n "$d" ] && [ "$d" != / ]; do
        if [ -d "$d" ]; then
            p=$(stat -c %a "$d" 2>/dev/null) || return 1
            case $p in '' | *[!0-7]*) return 1 ;; esac
            m=$((8#$p))
            # directories no one else can write are safe at any
            # ownership (this is how /home — root-owned, no sticky bit —
            # stays safe); only group/other-writable ones need care
            if [ $((m & 0022)) -ne 0 ]; then
                if [ -O "$d" ]; then
                    # ours but shared: a local attacker could swap our
                    # staged files — tighten unless the sticky bit
                    # already protects our entries
                    if [ $((m & 01000)) -eq 0 ]; then
                        chmod 700 "$d" 2>/dev/null || return 1
                    fi
                else
                    # someone else's writable directory: the sticky bit
                    # stops third parties renaming our entries, but the
                    # directory owner can still swap them (CWE-367) —
                    # accept only root-owned sticky directories like /tmp
                    [ $((m & 01000)) -ne 0 ] || return 1
                    o=$(stat -c %u "$d" 2>/dev/null) || return 1
                    [ "$o" = 0 ] || return 1
                fi
            fi
        fi
        d=${d%/*}
    done
}

cmd_auth() {
    [ "$(uname -s)" = Linux ] \
        || die "auth is implemented for Linux only; on $(uname -s) use 'gh auth login' (browser) or 'gh config set oauth_token --host github.com' with a token from your credential store"
    secure_hosts_yml
    gh auth status --hostname github.com --active >/dev/null 2>&1 && return 0
    command -v gh >/dev/null 2>&1 || die "gh not on PATH; run: setup_gh.sh install"
    # an invalid environment token takes precedence over anything we
    # store — fail before touching hosts.yml
    if { [ -n "${GH_TOKEN:-}" ] || [ -n "${GITHUB_TOKEN:-}" ]; } \
        && ! gh auth status --hostname github.com --active >/dev/null 2>&1; then
        die "GH_TOKEN/GITHUB_TOKEN is set but invalid; unset it or fix it, then retry"
    fi

    # keep xtrace from tracing the credential (CWE-532)
    local xtrace=0
    case $- in *x*) xtrace=1 ;; esac
    set +x

    local token dir f tmp bak
    # never prompt in unattended runs: a missing credential must be a
    # clean failure, not a wait for terminal or askpass input
    local cred
    if ! cred=$(env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE \
        GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 \
        git -c core.askPass= credential fill 2>/dev/null <<'EOF'
protocol=https
host=github.com
EOF
); then
        cred=""
    fi
    token=$(printf '%s\n' "$cred" | sed -n 's/^password=//p')
    [ -n "$token" ] || die "no github.com credential in the git credential store"

    dir=$(config_dir)
    case $dir in
        /*) ;;
        *) die "GH_CONFIG_DIR must be an absolute path (got: $dir)" ;;
    esac
    f="$dir/hosts.yml"
    mkdir -p "$dir" || die "cannot create $dir"
    ensure_private_dir "$dir" \
        || die "refusing to write credentials under $dir: path cannot be made user-private (check GH_CONFIG_DIR)"
    if [ -f "$f" ]; then
        # never silently overwrite: keep the old file under a unique,
        # non-overwriting name (mktemp: unique, mode 0600) for recovery
        bak=$(mktemp "$dir/hosts.yml.bak.XXXXXX")
        mv -f "$f" "$bak"
        log "previous hosts.yml kept at $bak"
    fi
    tmp=$(mktemp "$dir/hosts.yml.XXXXXX") # mktemp creates mode 0600
    printf 'github.com:\n    oauth_token: %s\n    git_protocol: https\n' "$token" >"$tmp"
    chmod 600 "$tmp"
    mv "$tmp" "$f"

    unset token
    [ "$xtrace" -eq 1 ] && set -x

    gh auth setup-git --hostname github.com >/dev/null 2>&1 \
        || die "gh auth setup-git failed; git HTTPS auth not configured"
}

cmd_verify() {
    local slug
    gh auth status --hostname github.com --active >/dev/null 2>&1 \
        || die "gh auth check failed"
    slug=$(repo_slug) || die "cannot resolve this repository from remote.origin.url"
    gh pr list --limit 1 --repo "$slug" >/dev/null 2>&1 \
        || die "cannot read PRs for this repository"
    log "gh configured"
}

main() {
    local cmd=${1:-all}
    case "$cmd" in
        check) cmd_check ;;
        install) cmd_install ;;
        auth) cmd_auth ;;
        verify) cmd_verify ;;
        all)
            if cmd_check; then
                exit 0
            fi
            if ! command -v gh >/dev/null 2>&1 \
                || ! gh auth status --help 2>/dev/null | grep -q -- --active; then
                cmd_install
            fi
            cmd_auth
            cmd_verify
            ;;
        *) die "usage: setup_gh.sh [check|install|auth|verify|all]" ;;
    esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
