---
name: release
description: Cut component releases and bundle releases. Use when publishing a component version, assembling a bundle, or preparing hotfixes and security rebuild waves.
---

# Release

Two-level versioning:

- **Component version** — independent semver per component, tagged on its
  container image (e.g. `gateway:v0.3.1`).
- **Bundle version** — a git tag `vX.Y.Z` plus a manifest pinning every
  component's exact version. A bundle release is a *tested combination
  snapshot*, not a full re-release of everything.

## Normal component release

1. Land changes through issues/PRs with conventional commits.
2. CI publishes the changed component's image under its new semver tag.
   Unchanged components are **not** rebuilt or republished.
3. Open a PR bumping only that component's pin in the bundle manifest.

## Bundle release

1. Ensure integration tests pass for the current manifest pins.
2. Bump the pins of every component that changed since the last bundle.
3. Tag `vX.Y.Z`; write release notes from conventional commits.
4. Publish the single bundle artifact (all components, one download).

## Exceptions

- **Hotfix**: patch-release the affected component, bump its pin, ship a
  patch bundle.
- **Security rebuild wave**: when a base image is patched, rebuild every
  component on that base as patch releases; this is a security process,
  not a regular release.

Rules of thumb: users only ever download whole bundles; component tags are
an internal CI mechanism and never user-facing.
