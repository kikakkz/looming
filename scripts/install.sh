#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# install.sh — one-command install of the `looming` CLI (AD-42, #191 S1).
#
# This script delivers the `looming` CLI binary and NOTHING else. The
# v0.1 cluster-in-one-shot installer contract is retired (AD-42
# decision 4): cluster bring-up is the CLI's own job — `looming apply
# --config <topology.yaml>` today, the agentic `looming setup`
# interview in a later slice (#191 S2a). The installer carries zero
# topology knowledge: no topology generation, no manifest download, no
# converge, no secrets, nothing under /etc.
#
# Contract: host preflight (docker + the compose plugin, auto-installed
# per #188) → resolve the release (default latest, --version pins) →
# download the arch-matched CLI → place it at the install destination →
# verify it runs (`looming --version`). Any preflight or download
# failure — and a binary that fails --version — exits nonzero.
#
# Usage:
#   install.sh [--version vX.Y.Z] [--release-url URL] [--prefix DIR]
#
# --prefix names the install destination directory for the binary
# (default ~/.local/bin, created when absent). It is NOT a state
# directory: nothing else is ever written. Root-owned system
# directories are refused for non-root users.
#
# One command (the operator's entry point) — download first, then run:
#   tmp="$(mktemp)" && curl -fsSL -o "$tmp" \
#       https://raw.githubusercontent.com/kikakkz/looming/main/scripts/install.sh && \
#     bash "$tmp"; rc=$?; rm -f "$tmp"
# (Execution is tied to a complete download: a pipe into bash would
# run whatever arrived, and a truncated download can half-run.)
#
# Release download base: the GitHub release of kikakkz/looming by
# default; --release-url (or LOOMING_RELEASE_URL) overrides it with a
# CLI-shaped mirror:
#   <base>/latest                       the latest release's tag
#   <base>/<tag>/looming-linux-amd64    the CLI binaries
#   <base>/<tag>/looming-linux-arm64
# (The bundle e2e suite drives the real installer through a file://
# mirror of exactly this shape.)

set -euo pipefail

REPO_SLUG="kikakkz/looming"

VERSION=""
PREFIX=""
RELEASE_URL="${LOOMING_RELEASE_URL:-}"

log() { printf '==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
install.sh — one-command install of the `looming` CLI (AD-42, #191 S1).

Usage:
  install.sh [--version vX.Y.Z] [--release-url URL] [--prefix DIR]

The script delivers the `looming` CLI binary and nothing else; cluster
bring-up is `looming apply --config <topology.yaml>` (the agentic
`looming setup` interview lands in a later slice, #191). Default
release source: the kikakkz/looming GitHub releases (--version pins a
tag; the default resolves the latest release). --release-url (or
LOOMING_RELEASE_URL) overrides the download base with a CLI-shaped
mirror:
  <base>/latest                     the latest release's tag
  <base>/<tag>/looming-linux-{amd64,arm64}

--prefix names the install destination directory for the binary
(default ~/.local/bin, created when absent) — nothing else is written
there, and root-owned system directories are refused for non-root
users.

One command — download first, then run:
  tmp="$(mktemp)" && curl -fsSL -o "$tmp" \
      https://raw.githubusercontent.com/kikakkz/looming/main/scripts/install.sh && \
    bash "$tmp"; rc=$?; rm -f "$tmp"
Execution is tied to a complete download: piping curl into bash would
run whatever arrived, and a failed or truncated download can half-run
or fail silently.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --version) VERSION="${2:?--version needs a value}"; shift 2 ;;
        --release-url) RELEASE_URL="${2:?--release-url needs a value}"; shift 2 ;;
        --prefix) PREFIX="${2:?--prefix needs a value}"; shift 2 ;;
        -h | --help) usage; exit 0 ;;
        *) die "unknown argument: $1 (see --help)" ;;
    esac
done

if [[ -n "$VERSION" && ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "--version must be a v-prefixed semver tag (vX.Y.Z), got: $VERSION"
fi
if [[ -n "$RELEASE_URL" ]]; then
    case "$RELEASE_URL" in
        https://* | file://*) ;;
        http://*)
            die "--release-url over plain http:// is refused: a network attacker could swap the downloaded CLI binary — use https://, or file:// for a local mirror: $RELEASE_URL"
            ;;
        *)
            die "--release-url must start with https:// or file://: $RELEASE_URL"
            ;;
    esac
    RELEASE_URL="${RELEASE_URL%/}"
fi

need_cmd() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }
fetch() { # $1 = URL, $2 = destination, $3 = transfer bound (seconds)
    curl -fsSL --retry 3 --max-time "$3" -o "$2" "$1"
}

# compose_autoinstall repairs a missing compose plugin instead of dying
# (#188): the installer runs `docker compose` as whichever user invoked
# it, and a user-level plugin (~/.docker/cli-plugins) is invisible to
# root under sudo — while a fresh host may have no plugin at all. Root
# promotes the invoking user's copy when present (zero download, version
# the operator already trusts); either way falls back to downloading
# the arch-matched binary from the docker/compose GitHub release.
compose_autoinstall() {
    local target_dir src arch url tmp
    if [ "$(id -u)" -eq 0 ]; then
        target_dir="${COMPOSE_PLUGIN_SYSTEM_DIR:-/usr/local/lib/docker/cli-plugins}"
        if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
            # DOCKER_CONFIG wins when sudo preserved it (sudo -E or
            # sudo DOCKER_CONFIG=... bash); otherwise the invoking
            # user's default ~/.docker is their active config dir.
            src="${DOCKER_CONFIG:-$(getent passwd "$SUDO_USER" | cut -d: -f6)/.docker}/cli-plugins/docker-compose"
            # -L guard: [ -f ] follows symlinks; copying one as root could
            # promote a file the user could not read themselves. The
            # `docker compose version` in the chain validates the copy: a
            # corrupt or wrong-arch plugin must not count as promoted.
            if [ -f "$src" ] && [ ! -L "$src" ]; then
                if mkdir -p "$target_dir" &&
                    cp "$src" "$target_dir/docker-compose" &&
                    chmod 755 "$target_dir/docker-compose" &&
                    docker compose version >/dev/null 2>&1; then
                    log "compose plugin: promoted $SUDO_USER's user-level install to $target_dir"
                    return 0
                fi
                rm -f "$target_dir/docker-compose"
                log "compose plugin: $SUDO_USER's copy is not usable — falling back to download"
            fi
        fi
    else
        target_dir="${DOCKER_CONFIG:-$HOME/.docker}/cli-plugins"
    fi
    case "$(uname -m)" in
        x86_64 | amd64) arch=x86_64 ;;
        aarch64 | arm64) arch=aarch64 ;;
        *) return 1 ;;
    esac
    url="https://github.com/docker/compose/releases/latest/download/docker-compose-linux-$arch"
    tmp="$(mktemp)" || return 1
    if fetch "$url" "$tmp" 300 && [ -s "$tmp" ]; then
        if mkdir -p "$target_dir" &&
            cp "$tmp" "$target_dir/docker-compose" &&
            chmod 755 "$target_dir/docker-compose"; then
            log "compose plugin: installed $url"
            rm -f "$tmp"
            return 0
        fi
        rm -f "$tmp"
        return 1
    fi
    rm -f "$tmp"
    return 1
}

# --- preflight ------------------------------------------------------------

log "preflight: docker"
need_cmd docker
if ! docker compose version >/dev/null 2>&1; then
    log "compose plugin missing — attempting install"
    compose_autoinstall ||
        die "the docker compose plugin is required and could not be installed (offline?) — see https://docs.docker.com/compose/install/linux/"
fi
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is required (docker compose version failed)"
docker info >/dev/null 2>&1 || die "the docker daemon is not reachable (docker info failed)"
need_cmd curl

# --- install destination ---------------------------------------------------

if [[ -z "$PREFIX" ]]; then
    PREFIX="${HOME:-}/.local/bin"
    [[ -n "$PREFIX" ]] || die "--prefix is required when HOME is not set"
fi
if [[ -d "$PREFIX" ]] && [[ "$(stat -c '%u' "$PREFIX")" == "0" ]] && [[ "$(id -u)" != "0" ]]; then
    die "--prefix $PREFIX is a root-owned system directory — re-run with sudo or pick a user-writable destination (the default is ~/.local/bin)"
fi
mkdir -p "$PREFIX" || die "cannot create $PREFIX — pick a writable install destination with --prefix"

# --- release resolution ---------------------------------------------------

if [[ -z "$VERSION" ]]; then
    log "resolving the latest $REPO_SLUG release"
    if [[ -n "$RELEASE_URL" ]]; then
        VERSION="$(fetch "$RELEASE_URL/latest" /dev/stdout 30)"
    else
        # The body lands complete in a variable BEFORE parsing: grep -m1
        # on a stream exits early and curl then dies on SIGPIPE, which
        # pipefail turns into a failed default install on a slow,
        # large response.
        body="$(curl -fsSL --retry 3 --max-time 30 \
            "https://api.github.com/repos/$REPO_SLUG/releases/latest")"
        VERSION="$(printf '%s\n' "$body" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
    fi
    [[ -n "$VERSION" ]] || die "could not resolve the latest release tag"
fi
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "release tag '$VERSION' is not a vX.Y.Z bundle version"

case "$(uname -s)" in
    Linux) ;;
    *) die "phase-1 installs are Linux-only; this host is $(uname -s)" ;;
esac
case "$(uname -m)" in
    x86_64) CLI_ASSET="looming-linux-amd64" ;;
    aarch64 | arm64) CLI_ASSET="looming-linux-arm64" ;;
    *) die "unsupported architecture for the released CLI: $(uname -m)" ;;
esac

if [[ -n "$RELEASE_URL" ]]; then
    ASSET_BASE="$RELEASE_URL/$VERSION"
else
    ASSET_BASE="https://github.com/$REPO_SLUG/releases/download/$VERSION"
fi

# --- download + verify ------------------------------------------------------

CLI="$PREFIX/looming"
log "downloading $CLI_ASSET from $ASSET_BASE"
if ! fetch "$ASSET_BASE/$CLI_ASSET" "$CLI.tmp" 600; then
    rm -f "$CLI.tmp"
    die "could not download $CLI_ASSET from $ASSET_BASE"
fi
mv "$CLI.tmp" "$CLI"
chmod 755 "$CLI"

log "verifying the installed binary"
if ! VERSION_OUT="$("$CLI" --version)"; then
    die "the installed binary does not run: $CLI --version failed"
fi

# --- summary ---------------------------------------------------------------

case ":$PATH:" in
    *":$PREFIX:"*) ;;
    *) printf "NOTE: %s is not on your PATH — add it in your shell profile (export PATH=\"%s:\$PATH\")\n" "$PREFIX" "$PREFIX" ;;
esac

cat <<EOF

The looming CLI $VERSION is installed.

  binary:  $CLI
$VERSION_OUT

Next steps:
  Bring up a cluster:  looming apply --config <topology.yaml>
  Add more hosts:      looming join http://<first-host>:<port> --token <token>

The agentic bring-up — an interview, an advisor-proposed topology,
staged bootstrap — lands as \`looming setup\` (#191, slice S2a).
EOF
