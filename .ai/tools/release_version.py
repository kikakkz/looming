#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Derive the next per-component release version.

AD-40 (#168) made the component-root `VERSION` file the canonical
version source (S2, #172): derivation reads that file — no git tag
fetch — while the pre-AD-40 tag derivation stays available as
`--from-tags` for compat and testing. The release flow built on this
is three steps (release-l1 §3.1/§5 S2): bump the VERSION file in a PR,
merge, then tag `<component>/<version>` at main; the release CI gate
refuses to publish when tag and file disagree.

Rules:
  - current version: read from the component's VERSION file (one
    `vX.Y.Z` line, comment/blank lines skipped; `--file` overrides the
    default `<component>/VERSION` lookup, `-` reads stdin). A missing
    file means the component was never released.
  - `--from-tags`: the component's existing tag strings; only
    `<prefix>/vX.Y.Z` shapes count, everything else is ignored (a stray
    tag never aborts a release). Callers pass one component's tags
    (git tag -l '<component>/v*').
  - no version at all -> v0.1.0 (the component's initial release).
  - tag mode: current version = the semver maximum (numeric field
    ordering — v0.1.10 sorts above v0.1.9), never string ordering.
  - patch bumps the third field; minor bumps the second and zeroes the
    third; major bumps the first and zeroes the second and third.

Errors: an unknown bump, a current version that is not vX.Y.Z, or —
in tag mode — a non-empty tag list holding zero valid versions raises
ValueError; the latter is a caller/pattern fault and must fail closed
rather than silently mint an initial version.

Usage:
  release_version.py --component gateway --bump patch
  release_version.py --component gateway --bump patch --file path/VERSION
  release_version.py --component gateway --bump patch --from-tags

Stdlib only; tag mode reads `git tag -l '<component>/v*'` from the cwd
repository. Without --bump the current version is printed instead of
the next one. Exit 0 with the version on stdout; exit 3 with empty
stdout when no version source exists (unreleased component); exit 1 on
failure, 2 on usage errors.
"""

import argparse
import re
import subprocess
import sys
from pathlib import Path

# One strict shape: three numeric fields behind a v. Pre-release/build
# suffixes (v1.0.0-beta) are not release versions — the release
# workflow's own guard accepts exactly this shape.
VERSION_RE = re.compile(
    r"^v(?P<major>\d+)\.(?P<minor>\d+)\.(?P<patch>\d+)$"
)
# One strict tag shape: a single-level prefix, a v, three numeric
# fields. Kept for the --from-tags fallback (the pre-AD-40 derivation).
TAG_RE = re.compile(
    r"^[A-Za-z0-9_-]+/v(?P<major>\d+)\.(?P<minor>\d+)\.(?P<patch>\d+)$"
)

BUMPS = ("patch", "minor", "major")


def _check_bump(bump):
    if bump not in BUMPS:
        raise ValueError(
            f"unknown bump {bump!r} (expected one of: {' '.join(BUMPS)})"
        )


def _bump_fields(current, bump):
    """The bumped (major, minor, patch) tuple from a version string."""
    m = VERSION_RE.match(current.strip())
    if not m:
        raise ValueError(f"current version {current!r} is not vX.Y.Z")
    cur = (int(m.group("major")), int(m.group("minor")),
           int(m.group("patch")))
    if bump == "patch":
        return (cur[0], cur[1], cur[2] + 1)
    if bump == "minor":
        return (cur[0], cur[1] + 1, 0)
    return (cur[0] + 1, 0, 0)


def bump_version(current, bump):
    """Return the next release version ("vX.Y.Z") from a current one.

    current: "vX.Y.Z" — or None/"" when the component was never
    released (any bump then lands on the initial v0.1.0).
    bump: "patch" (Z+1), "minor" (Y+1, Z reset to 0), or "major" (X+1,
    Y/Z reset to 0). Raises ValueError on an unknown bump or a current
    version that is not vX.Y.Z.
    """
    _check_bump(bump)
    if not current:
        return "v0.1.0"
    nxt = _bump_fields(current, bump)
    return f"v{nxt[0]}.{nxt[1]}.{nxt[2]}"


def next_version(tags, bump):
    """Return the next release version ("vX.Y.Z") for the component.

    tags: iterable of the component's existing tag strings (--from-tags
    path). bump: "patch" (Z+1), "minor" (Y+1, Z reset to 0), or "major"
    (X+1, Y/Z reset to 0).

    Empty tags -> "v0.1.0". Invalid tags are ignored. Raises ValueError
    on an unknown bump or when tags is non-empty but holds no valid
    version.
    """
    _check_bump(bump)
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
    nxt = _bump_fields(f"v{current[0]}.{current[1]}.{current[2]}", bump)
    return f"v{nxt[0]}.{nxt[1]}.{nxt[2]}"


def read_version_file(path):
    """Return the version from a component VERSION file (AD-40 source).

    None when the file does not exist (the component is unreleased).
    Raises ValueError when the file holds anything other than exactly
    one `vX.Y.Z` line: comment lines (#) and blank lines are skipped,
    so the repo's headered files parse to their single version line.
    `path` may be "-" to read stdin instead.
    """
    if str(path) == "-":
        text = sys.stdin.read()
    else:
        p = Path(path)
        if not p.is_file():
            return None
        text = p.read_text(encoding="utf-8")
    if not text.strip():
        return None  # empty input reads as "no version source"
    versions = []
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        versions.append(stripped)
    if len(versions) != 1 or not VERSION_RE.match(versions[0]):
        raise ValueError(
            f"{path}: expected exactly one vX.Y.Z version line, "
            f"got {versions!r}"
        )
    return versions[0]


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
    parser.add_argument("--bump", choices=BUMPS,
                        help="without it, print the current version "
                             "instead of the next one")
    parser.add_argument("--file",
                        help="VERSION file path (default: <component>/"
                             "VERSION under the cwd; '-' reads stdin)")
    parser.add_argument("--from-tags", action="store_true",
                        help="derive from git tags instead of the "
                             "VERSION file (pre-AD-40 fallback)")
    args = parser.parse_args(argv)

    try:
        if args.from_tags:
            try:
                tags = list_tags(args.component)
            except subprocess.CalledProcessError:
                print("ERROR: not inside a git repository.", file=sys.stderr)
                return 1
            if args.bump:
                print(next_version(tags, args.bump))
                return 0
            versions = []
            for tag in tags:
                m = TAG_RE.match(tag.strip())
                if m:
                    versions.append((int(m.group("major")),
                                     int(m.group("minor")),
                                     int(m.group("patch"))))
            if not versions:
                return 3
            cur = max(versions)
            print(f"v{cur[0]}.{cur[1]}.{cur[2]}")
            return 0
        path = args.file if args.file else str(Path(args.component) / "VERSION")
        try:
            current = read_version_file(path)
        except ValueError as exc:
            print(f"ERROR: {exc}", file=sys.stderr)
            return 1
        if current is None:
            if args.bump:
                print(bump_version(None, args.bump))
                return 0
            return 3
        print(bump_version(current, args.bump) if args.bump else current)
    except ValueError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
