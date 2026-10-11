---
number: 39
title: "release pipeline and artifact-only install"
date: "2026-10-11"
updated: "2026-10-11"
status: "accepted"
supersedes: []
adopted-at: "2026-10-11"
---

# AD-39 — release pipeline and artifact-only install

## Context

The release skill already fixes the versioning policy (component
semver on images; a bundle release as a git tag plus a manifest
pinning the tested combination; users download whole bundles), but
none of its mechanics existed: no registry publishing, no release
workflow, no manifest, no release assets — and the bundle had no
install path at all (#151). The first installer proposal built from
source at install time (checkout, container-built CLI, apply's
`--build` images); the maintainer rejected it outright (2026-10-11):
install-time building is irresponsible, and the install semantics are
explicit — a one-command install consumes released artifacts only.
Source builds remain the development workflow and the PR/e2e test
infrastructure (the suite verifies the code under change); neither is
an install path.

## Decision

The release skill's policy becomes mechanical (full mechanics in
`docs/architecture/release-l1.md`):

1. **Registry** — ghcr.io under the repository, immutable semver tags
   only (`ghcr.io/kikakkz/looming/<component>:vX.Y.Z`); CI pushes with
   `GITHUB_TOKEN`, pulls are anonymous. No mutable tags. The publish
   job enforces the two promises GHCR defaults would break: published
   packages are set public (verified by an unauthenticated pull after
   first publication), and an existing semver tag fails the job
   closed (pre-push manifest-inspect check, plus a per-tag concurrency
   group serializing inspect-and-push so concurrent same-tag releases
   collapse to one; both behaviors carry acceptance tests in R1).
2. **Tag-triggered releases** — component: `<component>/vX.Y.Z`; bundle:
   `vX.Y.Z`. One tag names one component; unchanged components are
   never rebuilt.
3. **Bundle manifest** — `bundle/manifest.yaml` in the repo pins each
   component's exact version; a component release is followed by a
   pin-bump PR; the bundle tag snapshots it.
4. **Artifact set** — component images (ghcr), the `looming` CLI as
   GH Release binaries per OS/arch (linux/amd64 + arm64 first), the
   manifest as a release asset, and `install.sh`. The CLI is a host
   binary and never ships as a container image.
5. **Render contract** — the render contract gains an optional image
   pin: pinned components render `image:` with no `build:`, and apply
   pulls — it never builds a pinned image. The unpinned source path
   (PR/e2e/dev) is unchanged. The topology file carries an optional
   `bundle:` section naming the release and its manifest.
6. **Artifact-only install** — `install.sh` resolves a release
   (default latest), downloads the CLI binary and manifest, generates
   the operator config (0600 secrets) under the install prefix, and
   converges via apply: zero build steps, no source mode.
7. **Rollback** is restore-from-backup (AD-36's pg_dump story);
   version downgrade of a live state plane is not supported.

Hotfixes and security rebuild waves keep the skill's exception flow.

## Consequences

- Slices R1–R4 (component CI, manifest + render pin, bundle CI,
  artifact installer) are filed from #153 after the design PR lands;
  the first release is the three components at v0.1.0 then bundle
  v0.1.0.
- `platform/go/render` changes are additive; cli and the e2e suite
  keep today's unpinned behavior.
- #151 is re-scoped to the artifact-only installer (blocked by this
  flow); the parked source-based prototype contributes only its
  config-generation and invite-handling logic.
- Digest pinning, image signing, release channels, and CLI
  darwin/windows assets are explicit non-goals for v1.
