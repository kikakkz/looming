---
name: gh-setup
description: Ensure the GitHub CLI (gh) is installed and authenticated for this repository. Use when gh is missing or unauthenticated, and before any workflow that drives PRs or issues (review loops, pr-watch, release).
---

# gh setup

Makes `gh` available for this repository's GitHub operations. If `gh`
is already installed and authenticated, do nothing and say so. Otherwise
install it user-locally (no root) and authenticate from the git
credential store. Never print the token; never write it into the
repository.

## 1. Check

```bash
command -v gh && gh auth status
```

Both succeed → done, nothing to install or configure.

## 2. Install (skip if `gh` is already on PATH)

Pinned release; bump the version, URL, and checksum together in one PR.

```bash
version=2.101.0
arch=$(uname -m); [ "$arch" = x86_64 ] && arch=amd64   # aarch64 -> arm64
name="gh_${version}_linux_${arch}"
url="https://github.com/cli/cli/releases/download/v${version}/${name}.tar.gz"

mkdir -p ~/.local/opt ~/.local/bin
curl -fL --retry 3 -o "/tmp/${name}.tar.gz" "$url"
echo '<sha256>  /tmp/'"$name"'.tar.gz' | sha256sum -c -   # per-arch sum, see release checksums
tar -xzf "/tmp/${name}.tar.gz" -C ~/.local/opt
ln -sfn ~/.local/opt/"$name"/bin/gh ~/.local/bin/gh
rm "/tmp/${name}.tar.gz"
```

- amd64 (x86_64) sha256 for v2.101.0:
  `9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8`
  (`gh_2.101.0_linux_amd64.tar.gz`; take other arches from the release's
  `gh_2.101.0_checksums.txt`).
- `~/.local/bin` must be on PATH (Ubuntu's `~/.profile` adds it at login
  when the directory exists; otherwise extend the shell rc).
- With root available, the apt repository from
  [cli.github.com](https://cli.github.com/) is the alternative.

## 3. Authenticate (skip if `gh auth status` already passes)

Bootstrap gh's own config from the git credential store. `gh auth
login --with-token` requires a `read:org`-scoped token, which a
repo-scoped PAT lacks — writing `hosts.yml` directly is the equivalent
path (it is gh's own store; gh itself writes it mode 0600).

```bash
if ! gh auth status >/dev/null 2>&1; then
  token=$(git credential fill <<'EOF' | sed -n 's/^password=//p'
protocol=https
host=github.com
EOF
)
  mkdir -p ~/.config/gh
  umask 077
  printf 'github.com:\n    oauth_token: %s\n    git_protocol: https\n' "$token" \
    > ~/.config/gh/hosts.yml
  unset token
fi
gh auth setup-git   # let git HTTPS operations use gh's auth
```

## 4. Verify

```bash
gh auth status
gh pr list --limit 1
```

Both succeed → configured. If `gh auth status` still fails, the stored
credential is missing or expired: fix `git credential fill` for
`github.com` first, then repeat step 3.

## Rules

- Never print the token, pass it on a command line, or commit anything
  under `~/.config/gh` or `~/.local`.
- The token inherits the git credential's scopes (`repo` is enough for
  every repo-level operation; org-level features need `read:org`).
- Install/configure only the invoking user's environment; do not use
  sudo or touch system paths.
