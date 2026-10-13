---
number: 40
title: "component-internal VERSION as the canonical version source"
date: "2026-10-13"
updated: "2026-10-13"
status: "accepted"
supersedes: []
adopted-at: "2026-10-13"
---

# AD-40 — component-internal VERSION as the canonical version source

## Context

Post-#153 review of the landed release pipeline (#159–#161): a
component's version exists only as a git tag. The component does not
self-describe — no binary answers `--version`, so an operator cannot
tell what build is running on a host — and version derivation depends
on fetching git tags. The maintainer ruled (2026-10-13, #168):
versions cohere inside the component (paradigm B), and every binary
gains a rich `--version` modeled on Go's `runtime/debug.BuildInfo` VCS
stamps (vcs.revision/vcs.time/vcs.modified), the goreleaser/promu
buildinfo shape, and the OCI image-label spec.

## Decision

1. **Component-internal VERSION is the single canonical version
   source.** Each component root carries a `VERSION` file — one line,
   `vX.Y.Z`, language-neutral (fits the AD-34 polyglot layout). The
   git tag `<component>/vX.Y.Z` and the image tag are marks and
   replicas of it, never independent truths.
2. **CI closes the double-truth risk.** The R1 component release job
   gains a consistency gate: the version resolved from the pushed tag
   must equal the component's VERSION content, else the job fails
   closed and nothing is published. This repo is AI-developed, so the
   constraint is also written into AGENTS.md and the release skill —
   AI contributors hold it by construction, not by checklist.
3. **Version derivation re-sources onto the file.** The release
   tooling reads VERSION files instead of resolving git tags (no tag
   fetch needed); the release flow becomes bump-VERSION PR → merge →
   tag, with release.sh stringing the three steps together.
4. **Rich `--version` is a standard.** A small shared buildinfo package
   carries version (ldflags-injected, default `dev`; CI reads it from
   VERSION) plus commit/buildTime/dirty, preferring the BuildInfo VCS
   stamps — local builds with `.git` self-report automatically, CI
   builds without `.git` fall back to injected values. Every binary
   (cmd/looming, cmd/gateway, cmd/identityd, cmd/topologyd) prints the
   promu-shaped block: version, commit (with dirty flag), build time,
   go version, platform. Dockerfiles take VERSION/COMMIT/BUILD_TIME
   build args into ldflags and carry the OCI labels
   (org.opencontainers.image.version/revision); the R1 component job
   and R3 bundle job pass them through, including to the CLI
   cross-compile.

## Consequences

- Slices S1–S3 are filed from #168 after the design PR lands: S1
  buildinfo + `--version` on the four binaries; S2 VERSION files +
  the R1 consistency gate + derivation re-source + the release.sh
  three-step flow; S3 Dockerfile/CI injection + OCI labels.
- The release flow gains a step: a bump-VERSION PR precedes the tag
  (release.sh automates PR → merge → tag); release_version and
  release.sh `--list` re-source onto the files.
- **Relation to AD-39:** AD-39's policy layer stands — two-level
  versions, artifact-only install, immutable tags, one tag names one
  component, unchanged components are not republished. This decision
  refines AD-39's mechanism layer: where a component's version lives,
  how it is derived, gated, and stamped. It does not supersede AD-39;
  the release L1 design (#153) carries the refined mechanics.
- The bundle manifest's role is unchanged: it stays the composition
  registry over component versions.
