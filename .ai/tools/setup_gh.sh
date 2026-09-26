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
    command -v gh >/dev/null 2>&1 || return 1
    gh auth status --help 2>/dev/null | grep -q -- --active || return 1
    gh auth status --hostname github.com --active >/dev/null 2>&1 || return 1
    gh pr list --limit 1 >/dev/null 2>&1 || return 1
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
    tmp=$(mktemp "/tmp/${name}.tar.gz.XXXXXX")
    curl -fL --retry 3 -o "$tmp" "$url" || { rm -f "$tmp"; die "download failed: $url"; }
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
    rm -rf "$target"
    mv "$tmpd/$name" "$target"
    rmdir "$tmpd" 2>/dev/null || true
    ln -sfn "$target/bin/gh" "$HOME/.local/bin/gh"
    hash -r 2>/dev/null || true
    # the binary selected from PATH must be the one just installed
    selected=$(command -v gh) || die "installed, but ~/.local/bin is not on PATH"
    [ "$(realpath "$selected")" = "$(realpath "$HOME/.local/bin/gh")" ] \
        || die "another gh shadows $HOME/.local/bin/gh (selected: $selected); fix PATH order"
}

cmd_auth() {
    secure_hosts_yml
    gh auth status --hostname github.com --active >/dev/null 2>&1 && return 0
    command -v gh >/dev/null 2>&1 || die "gh not on PATH; run: setup_gh.sh install"

    # keep xtrace from tracing the credential (CWE-532)
    local xtrace=0
    case $- in *x*) xtrace=1 ;; esac
    set +x

    local token dir f tmp bak
    token=$(git credential fill <<'EOF' | sed -n 's/^password=//p'
protocol=https
host=github.com
EOF
)
    [ -n "$token" ] || die "no github.com credential in the git credential store"

    dir=$(config_dir)
    f="$dir/hosts.yml"
    mkdir -p "$dir"
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
    gh auth status --hostname github.com --active >/dev/null 2>&1 \
        || die "gh auth check failed"
    gh pr list --limit 1 >/dev/null 2>&1 \
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
