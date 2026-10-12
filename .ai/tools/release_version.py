#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Derive the next per-component release version from existing tags.

Core of the interactive release driver (scripts/release.sh, #166): a
release version is never hand-typed — it is computed from the
component's existing `<component>/vX.Y.Z` tags (release-l1 §3.2: tag
conventions are the release trigger), so the driver menu can preview
both candidates before anything is pushed.

Rules:
  - tags: the component's existing tag strings; only `<prefix>/vX.Y.Z`
    shapes count, everything else is ignored (a stray tag never aborts
    a release). Callers pass one component's tags (git tag -l
    '<component>/v*').
  - no tags at all -> v0.1.0 (the component's initial release).
  - current version = the semver maximum (numeric field ordering —
    v0.1.10 sorts above v0.1.9), never string ordering.
  - patch bumps the third field; minor bumps the second and zeroes the
    third; major bumps the first and zeroes the second and third.

Errors: an unknown bump, or a non-empty tag list holding zero valid
versions, raises ValueError — the latter is a caller/pattern fault and
must fail closed rather than silently mint an initial version.

Usage:
  release_version.py --component gateway --bump patch

Stdlib only; reads tags from `git tag -l '<component>/v*'` in the
current repository. Exit 1 on failure, 2 on usage errors.
"""

import argparse
import re
import subprocess
import sys

# One strict shape: a single-level prefix, a v, three numeric fields.
# Pre-release/build suffixes (v1.0.0-beta) are not release tags — the
# release workflow's own guard accepts exactly this shape.
TAG_RE = re.compile(
    r"^[A-Za-z0-9_-]+/v(?P<major>\d+)\.(?P<minor>\d+)\.(?P<patch>\d+)$"
)

BUMPS = ("patch", "minor", "major")


def next_version(tags, bump):
    """Return the next release version ("vX.Y.Z") for the component.

    tags: iterable of the component's existing tag strings.
    bump: "patch" (Z+1), "minor" (Y+1, Z reset to 0), or "major" (X+1,
    Y/Z reset to 0).

    Empty tags -> "v0.1.0". Invalid tags are ignored. Raises ValueError
    on an unknown bump or when tags is non-empty but holds no valid
    version.
    """
    if bump not in BUMPS:
        raise ValueError(
            f"unknown bump {bump!r} (expected one of: {' '.join(BUMPS)})"
        )
    # Materialize: the emptiness check below must see the same input the
    # parse loop saw — a one-shot iterator would read as empty afterwards.
    tags = list(tags)
    versions = []
    for tag in tags:
        m = TAG_RE.match(tag.strip())
        if not m:
            continue
        versions.append(
            (int(m.group("major")), int(m.group("minor")), int(m.group("patch")))
        )
    if not versions:
        # Distinct cases: a component that was never released (empty
        # input) starts at v0.1.0; input that arrived but parsed to
        # nothing means the caller's filter is wrong — fail closed.
        if len(tags) == 0:
            return "v0.1.0"
        raise ValueError("no valid <component>/vX.Y.Z tag in the input")
    current = max(versions)
    if bump == "patch":
        nxt = (current[0], current[1], current[2] + 1)
    elif bump == "minor":
        nxt = (current[0], current[1] + 1, 0)
    else:
        nxt = (current[0] + 1, 0, 0)
    return f"v{nxt[0]}.{nxt[1]}.{nxt[2]}"


def list_tags(component):
    """Return the component's tag strings from the cwd's repository."""
    out = subprocess.run(
        ["git", "tag", "-l", f"{component}/v*"],
        capture_output=True, text=True, check=True,
    )
    return [line for line in out.stdout.splitlines() if line.strip()]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--component", required=True,
                        help="release name, e.g. gateway (manifest key)")
    parser.add_argument("--bump", required=True, choices=BUMPS)
    args = parser.parse_args(argv)

    try:
        tags = list_tags(args.component)
    except subprocess.CalledProcessError:
        print("ERROR: not inside a git repository.", file=sys.stderr)
        return 1
    try:
        print(next_version(tags, args.bump))
    except ValueError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
