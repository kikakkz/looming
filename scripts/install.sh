#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# install.sh — artifact-only one-command install of a released Looming
# bundle (#151, release-l1 §3.7).
#
# The installer consumes PUBLISHED RELEASE ARTIFACTS ONLY — the host's
# `looming` CLI binary and the release's bundle-manifest.yaml — and
# converges the bundle with `looming apply`, which pulls the pinned
# component images. There is no source-build install mode: developing
# from source is its own workflow (a checkout + the repo's dev
# tooling), never an install path.
#
# Flow: resolve the release (default latest, --version pins) → download
# the CLI (host OS/arch) + bundle-manifest.yaml → preflight Docker →
# materialize the operator config under the install prefix
# (topology.yaml plus the phase-1 secret env files, all 0600, fresh
# secrets on first install) → run `looming apply` → print the one-time
# bootstrap invite (AD-36) and the gateway's public guide URL.
#
# Ownership split (release-l1 §3.7/§3.8): the topology file's `bundle:`
# block is installer-owned, everything else is operator-owned. An
# existing topology.yaml is never clobbered: a newer --version rewrites
# ONLY the bundle: block and refreshes the manifest file (operator
# edits — ports, engine config, placements — survive); a matching
# version is a converge-only re-run.
#
# Usage:
#   install.sh --email admin@example.com \
#       --engine-url http://host.docker.internal:11434/v1 \
#       [--engine-key KEY] [--models gpt-5,qwen3] \
#       [--version v0.1.0] [--release-url URL] \
#       [--prefix /etc/looming] [--ports 18080,18081,18082,15432] \
#       [--name "my cluster"] [--private]
#
# One command (the operator's entry point):
#   curl -fsSL https://raw.githubusercontent.com/kikakkz/looming/main/scripts/install.sh \
#     | bash -s -- --email admin@example.com --engine-url ...
#
# Release download base: the GitHub release of kikakkz/looming by
# default; --release-url (or LOOMING_RELEASE_URL) overrides it with a
# release-shaped mirror:
#   <base>/latest                       the latest release's tag
#   <base>/<tag>/looming-linux-amd64    the CLI binaries
#   <base>/<tag>/looming-linux-arm64
#   <base>/<tag>/bundle-manifest.yaml   the pinned combination
# (The bundle e2e suite drives the real installer through a file://
# mirror of exactly this shape.)

set -euo pipefail

REPO_SLUG="kikakkz/looming"
POSTGRES_IMAGE="postgres:16-alpine"
COMPOSE_PROJECT="looming"

EMAIL=""
ENGINE_URL=""
ENGINE_KEY=""
MODELS=""
PREFIX="/etc/looming"
PORTS="18080,18081,18082,15432"
CLUSTER_NAME="looming cluster"
PRIVATE=0
VERSION=""
RELEASE_URL="${LOOMING_RELEASE_URL:-}"

log() { printf '==> %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
install.sh — artifact-only one-command install of a released Looming bundle (#151).

Usage:
  install.sh --email admin@example.com \
      --engine-url http://host.docker.internal:11434/v1 \
      [--engine-key KEY] [--models gpt-5,qwen3] \
      [--version v0.1.0] [--release-url URL] \
      [--prefix /etc/looming] [--ports 18080,18081,18082,15432] \
      [--name "my cluster"] [--private]

The installer consumes published release artifacts only (the host's
`looming` CLI binary + the release's bundle-manifest.yaml) and converges
the bundle with `looming apply`, which pulls the pinned component
images. Default release source: the kikakkz/looming GitHub releases
(--version pins a tag; the default is the latest release).
--release-url (or LOOMING_RELEASE_URL) overrides the download base with
a release-shaped mirror:
  <base>/latest                     the latest release's tag
  <base>/<tag>/looming-linux-{amd64,arm64}
  <base>/<tag>/bundle-manifest.yaml

Ownership split: the topology file's bundle: block is installer-owned;
everything else is operator-owned. A newer --version rewrites ONLY the
bundle: block + the manifest file; a matching version is a converge-only
re-run. Re-running is always safe — the topology file is never clobbered.

One command (the operator's entry point):
  curl -fsSL https://raw.githubusercontent.com/kikakkz/looming/main/scripts/install.sh \
    | bash -s -- --email admin@example.com --engine-url ...
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --email) EMAIL="${2:?--email needs a value}"; shift 2 ;;
        --engine-url) ENGINE_URL="${2:?--engine-url needs a value}"; shift 2 ;;
        --engine-key) ENGINE_KEY="${2:?--engine-key needs a value}"; shift 2 ;;
        --models) MODELS="${2:?--models needs a value}"; shift 2 ;;
        --version) VERSION="${2:?--version needs a value}"; shift 2 ;;
        --release-url) RELEASE_URL="${2:?--release-url needs a value}"; shift 2 ;;
        --prefix) PREFIX="${2:?--prefix needs a value}"; shift 2 ;;
        --ports) PORTS="${2:?--ports needs a value}"; shift 2 ;;
        --name) CLUSTER_NAME="${2:?--name needs a value}"; shift 2 ;;
        --private) PRIVATE=1; shift ;;
        -h | --help) usage; exit 0 ;;
        *) die "unknown argument: $1 (see --help)" ;;
    esac
done

[[ -n "$EMAIL" ]] || die "--email is required (the initial admin who receives the bootstrap invite)"
[[ "$EMAIL" =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]] || die "--email does not look like an email address: $EMAIL"
[[ -n "$ENGINE_URL" ]] || die "--engine-url is required (the OpenAI-compatible engine the gateway forwards to)"
case "$ENGINE_URL" in
    http://* | https://*) ;;
    *) die "--engine-url must start with http:// or https://: $ENGINE_URL" ;;
esac
if [[ -n "$VERSION" && ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "--version must be a v-prefixed semver tag (vX.Y.Z), got: $VERSION"
fi
if [[ -n "$RELEASE_URL" ]]; then
    case "$RELEASE_URL" in
        http://* | https://* | file://*) ;;
        *) die "--release-url must start with http://, https://, or file://: $RELEASE_URL" ;;
    esac
    RELEASE_URL="${RELEASE_URL%/}"
fi

IFS=',' read -r PORT_GATEWAY PORT_IDENTITY PORT_TOPOLOGY PORT_POSTGRES <<<"$PORTS"
for p in "$PORT_GATEWAY" "$PORT_IDENTITY" "$PORT_TOPOLOGY" "$PORT_POSTGRES"; do
    if ! [[ "$p" =~ ^[0-9]+$ ]] || ! ((p >= 1 && p <= 65535)); then
        die "--ports must be four comma-separated TCP ports, got: $PORTS"
    fi
done

# Containers reach a host-published engine through the host-gateway
# alias, never through the host's loopback names.
case "$ENGINE_URL" in
    *localhost* | *127.0.0.1* | *"[::1]"*)
        warn "engine URL points at the host's loopback, which is unreachable from containers"
        warn "rewriting the host to host.docker.internal (the e2e-proven channel)"
        ENGINE_URL="${ENGINE_URL//localhost/host.docker.internal}"
        ENGINE_URL="${ENGINE_URL//127.0.0.1/host.docker.internal}"
        ENGINE_URL="${ENGINE_URL//\[::1\]/host.docker.internal}"
        ;;
esac

UPSTREAM_INSECURE=""
if [[ "$ENGINE_URL" == http://* ]]; then
    warn "plain-HTTP engine URL: setting the gateway's trusted-network opt-out (upstream_insecure)"
    warn "engine credentials ride this link unencrypted — terminate TLS before real use"
    UPSTREAM_INSECURE='        upstream_insecure: "1"'
fi

need_cmd() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }
fetch() { # $1 = URL, $2 = destination, $3 = transfer bound (seconds)
    curl -fsSL --retry 3 --max-time "$3" -o "$2" "$1"
}

# --- preflight ------------------------------------------------------------

log "preflight: docker"
need_cmd docker
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is required (docker compose version failed)"
docker info >/dev/null 2>&1 || die "the docker daemon is not reachable (docker info failed)"
need_cmd curl
need_cmd openssl

# --- release resolution ---------------------------------------------------

if [[ -z "$VERSION" ]]; then
    log "resolving the latest $REPO_SLUG release"
    if [[ -n "$RELEASE_URL" ]]; then
        VERSION="$(fetch "$RELEASE_URL/latest" /dev/stdout 30)"
    else
        # Anonymous API view (public repo) — no gh auth required.
        VERSION="$(curl -fsSL --retry 3 --max-time 30 \
            "https://api.github.com/repos/$REPO_SLUG/releases/latest" \
            | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
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

# --- install prefix --------------------------------------------------------

if ! mkdir -p "$PREFIX/bin" 2>/dev/null; then
    die "cannot write $PREFIX — re-run with sudo, or pass --prefix <dir>"
fi
if [[ ! -d "$PREFIX/pgdata" ]]; then
    mkdir -p "$PREFIX/pgdata" || die "cannot write $PREFIX/pgdata — re-run with sudo, or pass --prefix <dir>"
    chmod 700 "$PREFIX/pgdata"
fi
# From here every materialized file is operator-secret-channel material.
umask 077

TOPOLOGY="$PREFIX/topology.yaml"
MANIFEST="$PREFIX/bundle-manifest.yaml"
FRESH=0
if [[ ! -f "$TOPOLOGY" ]]; then
    FRESH=1
fi

if [[ "$FRESH" -eq 1 ]]; then
    busy=()
    for p in "$PORT_GATEWAY" "$PORT_IDENTITY" "$PORT_TOPOLOGY" "$PORT_POSTGRES"; do
        if (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
            busy+=("$p")
        fi
    done
    ((${#busy[@]} == 0)) || die "port(s) already in use on this host: ${busy[*]} — free them or pass --ports"
    if docker ps -aq --filter "label=com.docker.compose.project=$COMPOSE_PROJECT" | grep -q .; then
        die "containers under the compose project '$COMPOSE_PROJECT' already exist — tear them down first"
    fi
fi

# --- release artifacts -----------------------------------------------------

log "downloading $CLI_ASSET + bundle-manifest.yaml from $ASSET_BASE"
fetch "$ASSET_BASE/$CLI_ASSET" "$PREFIX/bin/looming.tmp" 600
mv "$PREFIX/bin/looming.tmp" "$PREFIX/bin/looming"
chmod 755 "$PREFIX/bin/looming"

fetch "$ASSET_BASE/bundle-manifest.yaml" "$MANIFEST.tmp" 120
grep -q '^components:' "$MANIFEST.tmp" || die "downloaded bundle-manifest.yaml carries no components: pins missing at $ASSET_BASE"
mv "$MANIFEST.tmp" "$MANIFEST"
chmod 600 "$MANIFEST"

# --- operator config (topology.yaml + the secret env files) --------------

if [[ "$FRESH" -eq 1 ]]; then
    log "generating fresh secrets and writing the operator config to $PREFIX"
    PG_PASSWORD="$(openssl rand -hex 24)"
    IDENTITY_BOOTSTRAP_KEY="$(openssl rand -hex 24)"
    GATEWAY_IDENTITY_TOKEN="$(openssl rand -hex 24)"
    TOPOLOGY_SERVICE_TOKEN="$(openssl rand -hex 24)"
    KEY_MASTER_KEY="$(openssl rand -base64 32)"

    cat >"$PREFIX/postgres.env" <<EOF
POSTGRES_USER=looming
POSTGRES_PASSWORD=$PG_PASSWORD
EOF

    cat >"$PREFIX/identityd.env" <<EOF
IDENTITY_BOOTSTRAP_KEY=$IDENTITY_BOOTSTRAP_KEY
IDENTITY_GATEWAY_TOKEN=$GATEWAY_IDENTITY_TOKEN
IDENTITY_KEY_MASTER_KEY=$KEY_MASTER_KEY
EOF

    cat >"$PREFIX/gateway.env" <<EOF
GATEWAY_TOPOLOGY_TOKEN=$TOPOLOGY_SERVICE_TOKEN
GATEWAY_IDENTITY_TOKEN=$GATEWAY_IDENTITY_TOKEN
EOF

    cat >"$PREFIX/topologyd.env" <<EOF
TOPOLOGY_SERVICE_TOKEN=$TOPOLOGY_SERVICE_TOKEN
EOF

    ACCESS_MODE="public"
    [[ "$PRIVATE" -eq 1 ]] && ACCESS_MODE="private"

    CATALOG_LINE=""
    if [[ -n "$MODELS" ]]; then
        CATALOG_LINE="        catalog: \"$MODELS\""
    fi

    ENGINE_AUTH_LINE=""
    if [[ -n "$ENGINE_KEY" ]]; then
        ENGINE_AUTH_LINE="        upstream_auth: \"$ENGINE_KEY\""
    fi

    # Single-host loopback topology — the shape the bundle e2e proves:
    # the gateway reaches a host-published engine via the host-gateway
    # alias, and the loopback wiring triple (gateway extra_hosts +
    # paired service tokens) is what the config validator requires.
    # Image refs are deliberately absent: apply resolves the pinned
    # images from the bundle: block's manifest (release-l1 §3.5) — the
    # topology never names images.
    cat >"$TOPOLOGY" <<EOF
version: 1
access: {mode: $ACCESS_MODE, transport: direct, endpoint: "127.0.0.1"}
cluster: {name: $CLUSTER_NAME}
bootstrap: {admin_email: $EMAIL}
bundle:
  version: $VERSION
  manifest: $MANIFEST
state:
  postgres:
    image: $POSTGRES_IMAGE
    env_file: $PREFIX/postgres.env
    data_dir: $PREFIX/pgdata
    port: $PORT_POSTGRES
hosts:
  - {id: local, address: 127.0.0.1}
placements:
  - component: gateway-front
    host: local
    ports: {http: $PORT_GATEWAY}
    config:
        upstream: "$ENGINE_URL"
$ENGINE_AUTH_LINE
$UPSTREAM_INSECURE
        identity_url: "http://identityd:$PORT_IDENTITY"
        identity_insecure: "1"
$CATALOG_LINE
    env_file: $PREFIX/gateway.env
    extra_hosts: ["host.docker.internal:host-gateway"]
  - component: identityd
    host: local
    ports: {http: $PORT_IDENTITY}
    config:
        database_url: "postgres://looming:$PG_PASSWORD@bundle-postgres:5432/identity?sslmode=disable"
    env_file: $PREFIX/identityd.env
  - component: topologyd
    host: local
    ports: {http: $PORT_TOPOLOGY}
    config:
        database_url: "postgres://looming:$PG_PASSWORD@bundle-postgres:5432/topology?sslmode=disable"
    env_file: $PREFIX/topologyd.env
EOF
    chmod 600 "$PREFIX/postgres.env" "$PREFIX/identityd.env" "$PREFIX/gateway.env" "$PREFIX/topologyd.env"
    log "wrote $TOPOLOGY and the secret env files (0600)"
else
    # Ownership split (release-l1 §3.7/§3.8): the bundle: block is
    # installer-owned; the rest of the file — operator edits included —
    # passes through untouched.
    CURRENT="$(awk '
        /^bundle:/ { inblk=1; next }
        inblk && /^[^[:space:]]/ { inblk=0 }
        inblk && /^[[:space:]]*version:/ { sub(/^[[:space:]]*version:[[:space:]]*/, ""); print; exit }
    ' "$TOPOLOGY")"
    if [[ "$CURRENT" == "$VERSION" ]]; then
        log "already at $VERSION — converge-only re-run (the topology file is the operator's; nothing rewritten)"
    else
        if [[ -n "$CURRENT" ]]; then
            log "upgrading $CURRENT → $VERSION: rewriting the installer-owned bundle: block, preserving operator edits"
        else
            log "claiming this topology for $VERSION: adding the installer-owned bundle: block"
        fi
        awk -v ver="$VERSION" -v man="$MANIFEST" '
            BEGIN { inblk=0; wrote=0 }
            /^bundle:/ {
                print "bundle:"
                print "  version: " ver
                print "  manifest: " man
                inblk=1; wrote=1
                next
            }
            inblk && /^[^[:space:]]/ { inblk=0 }
            !inblk { print }
            END {
                if (!wrote) {
                    print "bundle:"
                    print "  version: " ver
                    print "  manifest: " man
                }
            }
        ' "$TOPOLOGY" >"$TOPOLOGY.tmp"
        mv "$TOPOLOGY.tmp" "$TOPOLOGY"
        chmod 600 "$TOPOLOGY"
    fi
fi

# --- converge --------------------------------------------------------------

log "converging the bundle with the downloaded CLI (apply pulls the pinned images)"
CLI="$PREFIX/bin/looming"
APPLY_LOG="$PREFIX/apply.log"
(
    cd "$PREFIX"
    "$CLI" apply --config "$TOPOLOGY" 2>&1 | tee "$APPLY_LOG"
)

INVITE_TOKEN="$(sed -n 's/^  token: //p' "$APPLY_LOG" | head -n 1)"
if [[ -n "$INVITE_TOKEN" ]]; then
    cat >"$PREFIX/bootstrap-invite.txt" <<EOF
# Looming bootstrap invite — one-time. Deliver it to the admin's mailbox,
# then delete this file. The admin registers with:
#   curl -sS -X POST http://127.0.0.1:$PORT_IDENTITY/v1/self/register \\
#     -H 'Content-Type: application/json' \\
#     -d '{"username":"<choose>","password":"<choose>","email":"$EMAIL","invite_token":"$INVITE_TOKEN"}'
token: $INVITE_TOKEN
EOF
    chmod 600 "$PREFIX/bootstrap-invite.txt"
else
    warn "the bootstrap invite did not come through (identityd may have been slow, or the admin already registered)"
    warn "recover it with: $CLI apply --config $TOPOLOGY --print-invite"
fi

# --- summary ---------------------------------------------------------------

case "$FRESH" in
    1) HEADLINE="Looming $VERSION is installed." ;;
    *)
        if [[ "${CURRENT:-}" == "$VERSION" ]]; then
            HEADLINE="Looming $VERSION converged (converge-only re-run — nothing was rewritten)."
        else
            HEADLINE="Looming upgraded ${CURRENT:-(unmanaged)} → $VERSION (operator edits preserved; only the bundle: block changed)."
        fi
        ;;
esac

cat <<EOF

$HEADLINE

  config:      $TOPOLOGY
  manifest:    $MANIFEST
  CLI:         $CLI
  invite:      $PREFIX/bootstrap-invite.txt (when the invite printed)
  guide page:  http://127.0.0.1:$PORT_GATEWAY/  (when access.public)

Next steps:
  1. Register the initial admin with the one-time invite above.
  2. Members self-register from the guide page (or an admin provisions them).
  3. Join more hosts: mint a token with
       $CLI token create --role engine --ttl 24h --database-url \\
         postgres://looming@127.0.0.1:$PORT_POSTGRES/topology?sslmode=disable
     (the Postgres password is in $PREFIX/postgres.env), then on the new host:
       $CLI join http://<this-host>:$PORT_TOPOLOGY --token <token> --host-id <id> --address <ip>

Re-apply after editing the topology file: $CLI apply --config $TOPOLOGY
Upgrade to a newer release: re-run this script with --version vX.Y.Z
  (rewrites only the installer-owned bundle: block + the manifest file)
Teardown: docker compose -p $COMPOSE_PROJECT down (the postgres data dir is $PREFIX/pgdata)
EOF
