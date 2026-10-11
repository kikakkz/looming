# SPDX-License-Identifier: Apache-2.0
"""release.yml static contract check — the R1 slice's local guard (#159).

The component release workflow cannot be exercised locally: a real tag
push against ghcr is its only honest harness (the workflow's own PR says
so). The deterministic half of the R1 contract is therefore checked
statically, in text — deliberately no YAML parser, so the assertions pin
only what R1 promises and survive refactors of surrounding structure.
Checks target behavior, not names:

  - the active entries under ``on.push.tags`` are exactly the three
    component patterns (commented-out patterns do not count — a stale
    pattern beside a changed active entry must fail)
  - permissions grant ``packages: write`` (GITHUB_TOKEN publishes)
  - the concurrency group is keyed by the *resolved* component+version,
    so same-tag runs serialize; a per-run key (``github.run_id``) does
    not
  - the ghcr.io/kikakkz/looming/ registry prefix appears
  - the workflow_dispatch ``verify_only`` input exists (the visibility
    recovery path, release-l1 §3.1(a))
  - the immutable pre-check fails closed (release-l1 §3.1(b)): it
    captures the inspect verdict and refuses an existing tag, matches
    the registry's explicit not-found verdicts as "tag absent", and
    exits non-zero on any other inspect error
  - ``resolve`` writes ``git_ref`` to ``$GITHUB_OUTPUT`` and maps it in
    the job outputs, and the checkout pins that ref — so a dispatch
    from an arbitrary branch still builds the named tag's sources

Usage: check_release_workflow.py [--workflow PATH]
Exit 0 when the contract holds, 1 listing what is missing.
"""

import argparse
import re
import sys
from pathlib import Path

EXPECTED_TAG_PATTERNS = ("gateway/v*.*.*", "identity/v*.*.*",
                         "topology/v*.*.*")
REGISTRY_PREFIX = "ghcr.io/kikakkz/looming/"
PACKAGES_WRITE_RE = re.compile(r"^\s*packages:\s*write\s*$", re.M)
VERIFY_INPUT_RE = re.compile(r"^\s*verify_only:", re.M)
GIT_REF_WRITE_RE = re.compile(
    r"git_ref=refs/tags/\S+\s*>>\s*\"\$GITHUB_OUTPUT\"", re.M)
GIT_REF_JOB_OUTPUT_RE = re.compile(
    r"^\s*git_ref:\s*\$\{\{[^}]*\.outputs\.git_ref\s*\}\}\s*$", re.M)
CHECKOUT_TAG_REF = "ref: ${{ needs.resolve.outputs.git_ref }}"
PRECHECK_STEP = "Immutable pre-check"
NOT_FOUND_MARKERS = ("no such manifest", "manifest unknown", "not found",
                     "NAME_UNKNOWN")


def _active_push_tags(text: str) -> list[str]:
    """Collect the literal patterns listed under ``on.push.tags``.

    Indentation-scoped line scan (the workflow's shape is fixed and
    simple); comment lines are skipped, so a pattern that survives only
    in a comment is not an active trigger.
    """
    entries: list[str] = []
    in_on = in_push = in_tags = False
    push_indent = tags_indent = -1
    for raw in text.splitlines():
        stripped_full = raw.strip()
        if not stripped_full or stripped_full.startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip())
        stripped = stripped_full.rstrip()
        if not in_on:
            if indent == 0 and stripped == "on:":
                in_on = True
            continue
        if indent == 0:
            break  # next top-level key ends the on: block
        if not in_push:
            if stripped == "push:":
                in_push, push_indent = True, indent
            continue
        if indent <= push_indent:
            break
        if not in_tags:
            if stripped == "tags:":
                in_tags, tags_indent = True, indent
            continue
        if indent <= tags_indent:
            break
        item = stripped[2:].strip() if stripped.startswith("- ") else ""
        if item:
            entries.append(item.strip("\"'"))
    return entries


def _concurrency_key(text: str) -> str | None:
    m = re.search(r"^\s*group:\s*(.+?)\s*$", text, re.M)
    return m.group(1) if m else None


def _step_run(text: str, step_name: str) -> str | None:
    """Extract a step's script: a ``run: |`` block body (dedented) or an
    inline ``run: <command>`` value. None when the step has no run."""
    lines = text.splitlines()
    for i, line in enumerate(lines):
        if line.strip() != f"- name: {step_name}":
            continue
        base = len(line) - len(line.lstrip())
        for j in range(i + 1, len(lines)):
            cand = lines[j]
            if not cand.strip() or cand.lstrip().startswith("#"):
                continue
            ind = len(cand) - len(cand.lstrip())
            if ind <= base:
                return None  # step ended without a run block
            stripped = cand.strip()
            if stripped.startswith("run:"):
                value = stripped[len("run:"):].strip()
                if value == "|":
                    body = []
                    for k in range(j + 1, len(lines)):
                        blk = lines[k]
                        if not blk.strip():
                            body.append("")
                            continue
                        bind = len(blk) - len(blk.lstrip())
                        if bind <= ind:
                            break
                        body.append(blk[ind + 1:])
                    return "\n".join(body)
                return value  # inline run: <command>
        return None
    return None


def _precheck_behavior_problems(script: str) -> list[str]:
    """The pre-check must refuse existing tags, accept explicit
    not-found verdicts, and fail closed on every other inspect error."""
    bad = []
    if "if out=$(docker manifest inspect" not in script:
        bad.append("pre-check does not capture the inspect verdict "
                   "(`if out=$(docker manifest inspect ...)` missing)")
    if "already exists" not in script:
        bad.append("pre-check never refuses an existing tag "
                   "(no 'already exists' refusal)")
    head, sep, tail = script.partition('case "$out"')
    if not sep:
        bad.append("pre-check has no not-found dispatch "
                   "(`case \"$out\"` missing)")
        return bad
    if "exit 1" not in head:
        bad.append("pre-check's existing-tag branch does not exit non-zero")
    if not any(marker in tail for marker in NOT_FOUND_MARKERS):
        bad.append("pre-check's dispatch matches no not-found verdict")
    default = re.search(r"^\s*\*\)", tail, re.M)
    if default is None:
        bad.append("pre-check has no default arm (`*)`)")
    elif "exit 1" not in tail[default.end():]:
        bad.append("pre-check does not fail closed on other inspect errors")
    return bad


def problems(text: str) -> list[str]:
    """Return the list of R1 contract pieces missing from the workflow."""
    missing = []
    active = _active_push_tags(text)
    for pattern in EXPECTED_TAG_PATTERNS:
        if pattern not in active:
            missing.append(
                f"active on.push.tags entry '{pattern}' is not declared")
    for entry in active:
        if entry not in EXPECTED_TAG_PATTERNS:
            missing.append(f"unexpected active on.push.tags entry '{entry}'")
    if not PACKAGES_WRITE_RE.search(text):
        missing.append("permissions do not grant `packages: write`")
    group = _concurrency_key(text)
    if group is None:
        missing.append("no concurrency group found")
    elif ("needs.resolve.outputs.component" not in group
          or "needs.resolve.outputs.version" not in group):
        missing.append("concurrency group is not keyed by the resolved "
                       "component+version")
    if REGISTRY_PREFIX not in text:
        missing.append(f"registry prefix {REGISTRY_PREFIX} absent")
    if not VERIFY_INPUT_RE.search(text):
        missing.append("workflow_dispatch `verify_only` input absent")
    script = _step_run(text, PRECHECK_STEP)
    if script is None:
        missing.append("immutable pre-check step absent")
    else:
        missing += _precheck_behavior_problems(script)
    if not GIT_REF_WRITE_RE.search(text):
        missing.append("resolve does not write `git_ref` to $GITHUB_OUTPUT")
    if not GIT_REF_JOB_OUTPUT_RE.search(text):
        missing.append("resolve job outputs do not map `git_ref`")
    if CHECKOUT_TAG_REF not in text:
        missing.append("checkout does not pin the resolved tag ref")
    return missing


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--workflow", default=".github/workflows/release.yml",
                    help="path to the release workflow (default: the "
                         "repo's .github/workflows/release.yml)")
    args = ap.parse_args(argv)
    path = Path(args.workflow)
    if not path.is_file():
        print(f"ERROR: {path} does not exist", file=sys.stderr)
        return 1
    missing = problems(path.read_text(encoding="utf-8"))
    if missing:
        for m in missing:
            print(f"ERROR: release.yml: {m}", file=sys.stderr)
        print(f"release-workflow: FAILED ({len(missing)} problem(s))",
              file=sys.stderr)
        return 1
    print("release-workflow: OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
