# Release L1 — module design

Derived in #153. Authority: the release skill
([.ai/skills/release/SKILL.md](../../.ai/skills/release/SKILL.md)) —
this design makes its policy mechanical; AD-39 records the decisions,
AD-40 the component version source (#168).
Vocabulary: [glossary](glossary.md).

## 1. Scope

How a bundle release is produced, published, and installed. The
versioning *policy* (two-level versions, tested-combination snapshots,
users download whole bundles) is the release skill's and does not
change here — this design fixes the mechanics: tag conventions, the
registry, the pinned manifest, the release artifacts, and the
artifact-only install path.

Fixed install semantics (maintainer, 2026-10-11): a one-command
install consumes **released artifacts only**. There is no source-build
install mode: developing from source is its own workflow (checkout +
the repo's dev tooling), not an install path. On-the-spot building at
install time is out by definition.

Survey anchors: GitLab omnibus/apt-style artifact distribution
(one published package per release, versioned by the combination),
goreleaser's tag-triggered multi-asset releases, ghcr as the
zero-infra registry for a GitHub-hosted project (GITHUB_TOKEN push,
anonymous pull).

## 2. Artifact model

Two-level versions, per the skill:

- **Component version** — independent semver per component. The
  canonical source is a `VERSION` file at the component root (single
  line `vX.Y.Z`, language-neutral); the git tag `<component>/vX.Y.Z`
  and the image tag `ghcr.io/kikakkz/looming/<component>:vX.Y.Z` are
  marks and replicas of it (#168, AD-40).
- **Bundle version** — a git tag `vX.Y.Z` plus a manifest pinning
  every component's exact version. A tested combination snapshot,
  never a full re-release.

A bundle release ships exactly these artifacts:

| Artifact | Form | Consumed by |
|---|---|---|
| Component images (`gateway`, `identity`, `topology`) | ghcr images, semver tags | apply pulls them at converge time |
| `looming` CLI | GH Release binaries, per OS/arch (linux-amd64, linux-arm64 first) | install.sh downloads the host's binary; admins and developers download directly |
| `bundle-manifest.yaml` | GH Release asset + `bundle/manifest.yaml` in the repo | apply resolves component pins at render time |
| `install.sh` | raw.githubusercontent.com (pinned by ref) + release asset | the operator's one command |

This table is the skill's "single bundle artifact (all components,
one download)" made concrete for a containerized product: the
operator downloads the release assets; the pinned image layers arrive
via registry pull at install time. One release, one installable
combination — never per-component downloads, and component tags stay
an internal CI mechanism (the skill's rule of thumb, kept verbatim).

The CLI is a **host binary, never a container**: apply orchestrates
the host's Docker, join collects the joining host's machine facts and
lands a 0600 credential, day-2 operations edit the host's
topology.yaml. The in-cluster half of those surfaces already lives in
the topologyd container; the CLI is the out-of-cluster half (cli-l1).
CLI container images are rejected — they recreate host-orchestration
problems inside a container for no operator benefit.

Three surfaces, three semantics — kept strictly separate:

| Surface | Source of bits | Why |
|---|---|---|
| PR / e2e CI | source build (apply's `--build`) | the suite verifies the code under change; published artifacts would test the past |
| release CI | source build once, published as artifacts | the tested combination becomes the artifact set |
| install | published artifacts only | operators consume what was tested, nothing else |

## 3. Decisions

1. **Registry.** ghcr.io under the repository
   (`ghcr.io/kikakkz/looming/<component>`). CI pushes with
   `GITHUB_TOKEN` (`packages: write`); pulls are anonymous. Only
   immutable semver tags — NO `:latest`, NO mutable tags. Two
   mechanics the publish job (R1) enforces, because GHCR defaults
   fight both promises: (a) *package visibility* — new packages
   default to private, which would break the installer's anonymous
   pulls; GitHub documents no supported API for flipping visibility,
   so CI does not automate it — instead the job verifies an
   unauthenticated pull after first publication and fails, pointing
   at the one-time owner action (the package's settings page → make
   public), until the package is public — recovery is a job re-run
   after the owner action (a `workflow_dispatch` verify mode
   re-executes the anonymous-pull check; the tag pre-check is
   unchanged, so a re-run can never repoint a published tag);
   (b) *tag immutability* — GHCR has no native
   enforcement and manifests pin tags, not digests, so an overwritten
   tag would silently repoint a released bundle; the job fails closed
   when the semver tag already exists (a pre-push `docker manifest
   inspect` check), a per-tag concurrency group serializes the
   inspect-and-push sequence so two simultaneous same-tag releases
   collapse to one (the second then fails the check), and R1's
   acceptance proves both: a double push to the same semver tag in a
   disposable package must reject, and two parallel publishers racing
   at one tag must land exactly one. Component images publish as
   multi-arch manifest lists — linux/amd64 + linux/arm64 (QEMU-backed
   buildx in the publish job) — matching the CLI's platform matrix, so
   an arm64 host never downloads a CLI it cannot run components for.
   (Digest-pinned manifests remain the later hardening — §4.)

   A component's version is canonical in the component, not on the tag:
   AD-40 designates a `VERSION` file at the component root (single
   line `vX.Y.Z`, language-neutral) as the single source of truth; the
   git tag and the image tag mark and replicate it (#168). Once S2
   lands, the publish job enforces a third mechanic alongside (a) and
   (b) — (c) *version consistency*: the job resolves the pushed tag's
   version and fails closed when it differs from the component's
   VERSION file, so tag and file can never disagree on what was
   released. Until S2, the tag stays the operational version source —
   the driver still derives from git tags and nothing gates the match.
   The double-truth risk the file invites is closed by CI once (c) is
   live, and — this repo being AI-developed — the constraint is also
   written into AGENTS.md and the release skill, so AI contributors
   hold it by construction.
2. **Tag conventions are the release trigger.** Component release:
   push `<component>/vX.Y.Z` (e.g. `gateway/v0.1.0`). Bundle release:
   push `vX.Y.Z`. The release workflows match on these tag shapes;
   there is no manual dispatch in v1.
3. **Component release CI** builds only the tagged component's image
   from the tagged commit and pushes it under its semver tag.
   Unchanged components are not rebuilt or republished (skill rule,
   enforced by construction — one tag names one component).
4. **Bundle manifest.** `bundle/manifest.yaml` on main is the source
   of truth for the current pins:

   ```yaml
   # Released combination pins (release skill).
   components:
     gateway: v0.1.0
     identity: v0.1.0
     topology: v0.1.0
   ```

   A component release is followed by a PR bumping only that
   component's pin; the bundle tag snapshots the manifest. Pins are
   semver **tags** (the skill's "exact version" semantic — tags under
   our control are immutable); digest pinning is recorded as future
   hardening, not v1.
5. **Render/apply contract.** `platform/go/render`'s component
   contract gains an optional image pin. A pinned component renders
   `image: <pinned ref>` and NO `build:` block; apply pulls and
   converges — it never builds a pinned image. Unpinned components
   keep today's `build:` path unchanged (PR/e2e and source
   development). A fully pinned render carries no build context at
   all, so apply skips build-context digesting for it — an
   artifact-installed host has no source tree, and `--bundle-root`
   stays a source-path concern (#151 R4). The topology file gains an
   optional `bundle:`
   section, written by the installer:

   ```yaml
   bundle: {version: v0.1.0, manifest: /etc/looming/bundle-manifest.yaml}
   ```

   Apply validates that the manifest covers every placed component and
   fails closed otherwise. The topology file stays the desired-state
   source (AD-36 semantics unchanged).
6. **Bundle release CI**, on `vX.Y.Z`: verify every manifest pin is
   pullable → pull the pinned images and run the integration suite
   against them (the skill's "ensure integration tests pass for the
   current manifest pins" — pullability does not prove compatibility,
   so publication gates on this) → cross-compile the CLI
   (`CGO_ENABLED=0`, linux/amd64 + linux/arm64) → create the GitHub
   Release with notes generated from conventional commits and the
   asset set from §2. Ordering rule: component tags at or before the
   bundle tag; the bundle workflow fails if a pin is missing or the
   pinned-image integration run is red.
7. **Installer flow (#151, slice R4).** `install.sh`:
   resolve the release (default latest; `--version vX.Y.Z` pins) →
   download the CLI binary (host OS/arch) and `bundle-manifest.yaml`
   → preflight Docker (daemon + compose plugin) → generate the
   operator config under the install prefix (topology.yaml + the
   phase-1 secret env files, all 0600, fresh secrets — the generation
   logic prototyped on `feat/151-one-click-install`) → run
   `looming apply`, which pulls the pinned images and converges →
   print the one-time bootstrap invite (AD-36) and the guide URL.
   Zero build steps. Ownership split: the `bundle:` block is
   installer-owned, everything else in topology.yaml is
   operator-owned. Fresh install writes the whole file; an existing
   topology.yaml is never clobbered — but when the requested release
   differs from the file's `bundle.version` (an upgrade), the
   installer rewrites ONLY the installer-owned `bundle:` block and
   refreshes the manifest file, preserving operator edits (ports,
   engine config, placements); a matching version is a converge-only
   re-run (edit + re-apply).
8. **Upgrade and rollback.** Upgrade: re-run the installer at the new
   version — it moves the topology's `bundle:` selection to the new
   release (installer-owned block only, §3.7) and refreshes the
   manifest — then apply converges to the new pins; schema migrations
   under the components move forward only. Rollback: restore from the
   pg_dump-based backup (AD-36's backup story) — version downgrade of
   a live state plane is not supported.
9. **Exceptions** — hotfixes and security rebuild waves follow the
   skill: identical tag mechanics, patch versions, pin-bump PRs.

## 4. Non-goals (v1)

- Digest-pinned manifests (tag pins now; hardening later).
- Image signing / provenance attestations (cosign etc.).
- Release channels (stable/edge) and automatic update checks.
- CLI assets beyond linux/amd64 + linux/arm64 (darwin/windows on
  demand).
- Air-gapped registry mirrors — an ops concern outside the v1 flow;
  the manifest's image refs are the single override point.
- Publishing unchanged components "to be safe" — explicitly rejected
  by the skill and by construction here.

## 5. Slices

Filed as issues after this design lands (the #107 pattern).

- **R1 — component release CI.** `release.yml` component job; cutting
  `gateway/v0.1.0`, `identity/v0.1.0`, `topology/v0.1.0` publishes the
  three images. Acceptance: tag push publishes; anonymous pull works;
  one tag builds one component; each published package is public
  (unauthenticated pull verified after first publication); the
  double-push test — two pushes to the same semver tag in a
  disposable package — rejects the second; the concurrent-publisher
  test — two parallel publishes racing at one tag — lands exactly one;
  and the visibility recovery path — publish against a private package,
  flip visibility via the owner action, re-run the verify mode, and the
  anonymous pull passes without touching the tag; the image manifest
  lists both linux/amd64 and linux/arm64 (per-platform pull checks).
- **R2 — manifest + render pin.** `bundle/manifest.yaml`; the render
  contract's image pin; apply validation; the `bundle:` topology
  section. Acceptance: apply with a manifest renders `image:` refs and
  never builds; the unpinned source path is byte-identical in behavior
  (e2e proof); unit + integration + e2e green.
- **R3 — bundle release CI.** The `vX.Y.Z` job: pin verification, the
  pinned-image integration run (pull the manifest's images, run the
  integration suite against them — publication gates on green), CLI
  cross-compile, GH Release with notes + assets. Acceptance: tagging
  `v0.1.0` produces a release whose assets install-verify in R4's
  harness; a red pinned-image integration run blocks the release.
- **R4 — artifact-only installer (#151, revised).** The §3.7 flow.
  e2e verification: the suite publishes its built images to a local
  registry fixture, assembles a release-shaped download fixture, and
  runs the real install.sh against it — proving the artifact path end
  to end without touching ghcr. Acceptance: one command on a fresh
  Linux host with Docker, zero builds; the installed combination is
  exactly the release's pins.
- **First release.** Cut the three component tags then `v0.1.0`;
  verify the published artifacts with the R4 installer on a clean
  host.

Mechanism refinements from #168 (AD-40), filed as issues the same way:

- **S1 — rich `--version` everywhere.** `platform/go/buildinfo`:
  version (ldflags-injected, default `dev`) + commit/buildTime/dirty,
  preferring the BuildInfo VCS stamps so local `.git` builds
  self-report while CI builds without `.git` fall back to injected
  values; `--version` on the four binaries (cmd/looming, cmd/gateway,
  cmd/identityd, cmd/topologyd) printing the promu-shaped block
  (version, commit + dirty, build time, go, platform). Acceptance:
  every binary prints the block; unit tests cover the dev default,
  injected values, and the BuildInfo-preferred path.
- **S2 — VERSION files + consistency gate + re-sourced derivation.**
  The three component roots gain `VERSION` (v0.1.0); the R1 job fails
  closed when the pushed tag's version != the file's content;
  release_version and `release.sh --list` read the files (no git tag
  fetch); release.sh strings the three-step flow (bump-VERSION PR →
  merge → tag). Acceptance: a mismatched tag push rejects before any
  image push; `--list` works on a tagless clone.
- **S3 — image/CI stamping + OCI labels.** Dockerfiles take
  VERSION/COMMIT/BUILD_TIME build args into ldflags and carry the OCI
  labels (org.opencontainers.image.version/revision); R1's component
  job and R3's CLI cross-compile pass the values. Acceptance: the
  published image carries the labels at the tag's version; the
  released CLI's `--version` reports version and commit matching the
  release.

## 6. Consequences

- `platform/go/render` changes are additive; both consumers (cli's
  apply, and the e2e suite through it) keep today's unpinned path.
- The release skill stays the policy record; this design is its
  mechanics. AGENTS.md's release pointer gains release-l1 alongside
  the skill.
- Install-related docs (#152's README sweep and the Install section
  from the parked installer branch) rebase onto this design: the
  README Install section documents the artifact command only.
