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
# version is a converge-only re-run. --config-only stops after the
# config stage, before the CLI download and converge.
#
# Usage:
#   install.sh --email admin@example.com \
#       --engine-url http://host.docker.internal:11434/v1 \
#       [--engine-key KEY] [--models gpt-5,qwen3] \
#       [--version v0.1.0] [--release-url URL] \
#       [--prefix /etc/looming] [--ports 18080,18081,18082,15432] \
#       [--name "my cluster"] [--private] [--config-only]
#
# One command (the operator's entry point) — download first, then run:
#   tmp="$(mktemp)" && curl -fsSL -o "$tmp" \
#       https://raw.githubusercontent.com/kikakkz/looming/main/scripts/install.sh && \
#     bash "$tmp" --email admin@example.com --engine-url ...; rc=$?; rm -f "$tmp"
# (Execution is tied to a complete download: a pipe into bash would
# run whatever arrived, and a truncated download can half-run.)
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
CONFIG_ONLY=0
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
      [--name "my cluster"] [--private] [--config-only]

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

One command (the operator's entry point) — download first, then run:
  tmp="$(mktemp)" && curl -fsSL -o "$tmp" \
      https://raw.githubusercontent.com/kikakkz/looming/main/scripts/install.sh && \
    bash "$tmp" --email admin@example.com --engine-url http://host.docker.internal:11434/v1; \
  rc=$?; rm -f "$tmp"
Execution is tied to a complete download: piping curl into bash would
run whatever arrived, and a failed or truncated download can half-run
or fail silently.
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
        --config-only) CONFIG_ONLY=1; shift ;;
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

IFS=',' read -r PORT_GATEWAY PORT_IDENTITY PORT_TOPOLOGY PORT_POSTGRES <<<"$PORTS"
seen=" "
for p in "$PORT_GATEWAY" "$PORT_IDENTITY" "$PORT_TOPOLOGY" "$PORT_POSTGRES"; do
    if ! [[ "$p" =~ ^[0-9]+$ ]] || ! ((p >= 1 && p <= 65535)); then
        die "--ports must be four comma-separated TCP ports, got: $PORTS"
    fi
    if [[ "$seen" == *" $p "* ]]; then
        die "--ports must be four distinct ports, got a duplicate: $p (in: $PORTS)"
    fi
    seen="$seen$p "
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

# yaml_sq single-quote-encodes one scalar for the flow-style YAML the
# generated topology uses. Doubling every embedded ' is the whole
# escape, so any operator input — colons, quotes, spaces, empties —
# lands as a valid YAML scalar (CodeRabbit #165: a --name like
# "Production: east" used to write an unparseable flow scalar, and
# re-runs then preserved the broken file). Values bound for env files
# never pass through here: env files are not YAML.
yaml_sq() {
    printf "'%s'" "${1//\'/\'\'}"
}

# topology_http_port echoes the published http port of one placement in
# the operator's topology (the installer-generated `ports: {http: N}`
# shape); empty when the placement or port is absent.
topology_http_port() { # $1 = component name
    awk -v comp="$1" '
        $0 ~ "- component: " comp "$" { want=1 }
        want && /ports: \{http: [0-9]+\}/ {
            match($0, /http: [0-9]+/)
            print substr($0, RSTART + 6, RLENGTH - 6)
            exit
        }
    ' "$TOPOLOGY"
}

# topology_pg_port echoes state.postgres.port from the topology; empty
# when the section is absent.
topology_pg_port() {
    awk '
        /^  postgres:/ { inpg=1; next }
        inpg && /^  [^[:space:]]/ { inpg=0 }
        inpg && $1 == "port:" { print $2; exit }
    ' "$TOPOLOGY"
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
        # Anonymous API view (public repo) — no gh auth required. The
        # body lands complete in a variable BEFORE parsing: grep -m1 on
        # a stream exits early and curl then dies on SIGPIPE, which
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

# The manifest lands before the config stage: the topology's bundle:
# block references this file, so even --config-only leaves the two
# consistent (a later full run re-downloads it idempotently).
log "downloading bundle-manifest.yaml from $ASSET_BASE"
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
        CATALOG_LINE="        catalog: $(yaml_sq "$MODELS")"
    fi

    ENGINE_AUTH_LINE=""
    if [[ -n "$ENGINE_KEY" ]]; then
        ENGINE_AUTH_LINE="        upstream_auth: $(yaml_sq "$ENGINE_KEY")"
    fi

    # Single-host loopback topology — the shape the bundle e2e proves:
    # the gateway reaches a host-published engine via the host-gateway
    # alias, and the loopback wiring triple (gateway extra_hosts +
    # paired service tokens) is what the config validator requires.
    # Image refs are deliberately absent: apply resolves the pinned
    # images from the bundle: block's manifest (release-l1 §3.5) — the
    # topology never names images. Every interpolated operator value
    # passes through yaml_sq: single-quoted scalars survive any
    # character the operator throws at them.
    cat >"$TOPOLOGY" <<EOF
version: 1
access: {mode: $ACCESS_MODE, transport: direct, endpoint: "127.0.0.1"}
cluster: {name: $(yaml_sq "$CLUSTER_NAME")}
bootstrap: {admin_email: $(yaml_sq "$EMAIL")}
bundle:
  version: $VERSION
  manifest: $(yaml_sq "$MANIFEST")
state:
  postgres:
    image: $(yaml_sq "$POSTGRES_IMAGE")
    env_file: $(yaml_sq "$PREFIX/postgres.env")
    data_dir: $(yaml_sq "$PREFIX/pgdata")
    port: $PORT_POSTGRES
hosts:
  - {id: local, address: 127.0.0.1}
placements:
  - component: gateway-front
    host: local
    ports: {http: $PORT_GATEWAY}
    config:
        upstream: $(yaml_sq "$ENGINE_URL")
$ENGINE_AUTH_LINE
$UPSTREAM_INSECURE
        identity_url: $(yaml_sq "http://identityd:$PORT_IDENTITY")
        identity_insecure: "1"
$CATALOG_LINE
    env_file: $(yaml_sq "$PREFIX/gateway.env")
    extra_hosts: ["host.docker.internal:host-gateway"]
  - component: identityd
    host: local
    ports: {http: $PORT_IDENTITY}
    config:
        database_url: $(yaml_sq "postgres://looming:$PG_PASSWORD@bundle-postgres:5432/identity?sslmode=disable")
    env_file: $(yaml_sq "$PREFIX/identityd.env")
  - component: topologyd
    host: local
    ports: {http: $PORT_TOPOLOGY}
    config:
        database_url: $(yaml_sq "postgres://looming:$PG_PASSWORD@bundle-postgres:5432/topology?sslmode=disable")
    env_file: $(yaml_sq "$PREFIX/topologyd.env")
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
        # The manifest path is pre-encoded (yaml_sq) so the rewritten
        # block matches the fresh-install shape byte-for-byte — a path
        # with spaces or quotes must not parse differently depending
        # on which install path wrote it.
        man="$(yaml_sq "$MANIFEST")"
        awk -v ver="$VERSION" -v man="$man" '
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

# --- effective ports ---------------------------------------------------------

# The guide/register/join hints below must name the ports that are
# actually published. A fresh install publishes this run's --ports; a
# re-run or an upgrade preserves the operator's topology file — its
# ports win, and a re-run passing different --ports must not print
# those (CodeRabbit #165 round 2). Values absent from the file fall
# back to this run's parameters.
EFF_GATEWAY="$PORT_GATEWAY"
EFF_IDENTITY="$PORT_IDENTITY"
EFF_TOPOLOGYD="$PORT_TOPOLOGY"
EFF_POSTGRES="$PORT_POSTGRES"
if [[ "$FRESH" -eq 0 ]]; then
    EFF_GATEWAY="$(topology_http_port gateway-front)"
    [[ -n "$EFF_GATEWAY" ]] || EFF_GATEWAY="$PORT_GATEWAY"
    EFF_IDENTITY="$(topology_http_port identityd)"
    [[ -n "$EFF_IDENTITY" ]] || EFF_IDENTITY="$PORT_IDENTITY"
    EFF_TOPOLOGYD="$(topology_http_port topologyd)"
    [[ -n "$EFF_TOPOLOGYD" ]] || EFF_TOPOLOGYD="$PORT_TOPOLOGY"
    EFF_POSTGRES="$(topology_pg_port)"
    [[ -n "$EFF_POSTGRES" ]] || EFF_POSTGRES="$PORT_POSTGRES"
fi

# --- converge --------------------------------------------------------------

if [[ "$CONFIG_ONLY" -eq 1 ]]; then
    log "config only — converging downloads the CLI and runs apply; re-run without --config-only, or converge manually once a CLI is on the PATH:"
    printf '    looming apply --config %s\n' "$TOPOLOGY"
    exit 0
fi

log "downloading $CLI_ASSET from $ASSET_BASE"
fetch "$ASSET_BASE/$CLI_ASSET" "$PREFIX/bin/looming.tmp" 600
mv "$PREFIX/bin/looming.tmp" "$PREFIX/bin/looming"
chmod 755 "$PREFIX/bin/looming"

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
#   curl -sS -X POST http://127.0.0.1:$EFF_IDENTITY/v1/self/register \\
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
  guide page:  http://127.0.0.1:$EFF_GATEWAY/  (when access.public)

Next steps:
  1. Register the initial admin with the one-time invite above.
  2. Members self-register from the guide page (or an admin provisions them).
  3. Join more hosts: mint a token (the subshell sources the 0600
     $PREFIX/postgres.env — the password reaches the command from the
     file, never from shell history):
       ( set -a; . "$PREFIX/postgres.env"; set +a; $CLI token create --role engine --ttl 24h --database-url \\
         "postgres://\$POSTGRES_USER:\$POSTGRES_PASSWORD@127.0.0.1:$EFF_POSTGRES/topology?sslmode=disable" )
     then on the new host:
       $CLI join http://<this-host>:$EFF_TOPOLOGYD --token <token> --host-id <id> --address <ip>

Re-apply after editing the topology file: $CLI apply --config $TOPOLOGY
Upgrade to a newer release: re-run this script with --version vX.Y.Z
  (rewrites only the installer-owned bundle: block + the manifest file)
Teardown: docker compose -p $COMPOSE_PROJECT down (the postgres data dir is $PREFIX/pgdata)
EOF
