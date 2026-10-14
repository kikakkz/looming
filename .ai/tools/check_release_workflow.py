# SPDX-License-Identifier: Apache-2.0
"""release.yml static contract check — R1's local guard (#159), extended
by the R3 bundle-release contract (#161).

The release workflow cannot be exercised locally: a real tag push
against ghcr is its only honest harness (the workflow's own PR says
so). The deterministic half of the contract is therefore checked
statically, in text — deliberately no YAML parser, so the assertions
pin only what the slices promise and survive refactors of surrounding
structure. Checks target behavior, not names:

  - the active entries under ``on.push.tags`` are exactly the three
    component patterns plus the bare bundle ``v*.*.*`` (commented-out
    patterns do not count — a stale pattern beside a changed active
    entry must fail)
  - the trigger block keeps its disjointness note: a tag filter's
    ``*`` never crosses ``/``, so ``v*.*.*`` cannot match a
    component tag and the component patterns cannot match a bare
    bundle tag (release-l1 §3.2)
  - the component jobs declare a guard limiting them to dispatch and
    component-shaped tags; the bundle job declares the complementary
    bare-tag guard, and the bundle guard sits on the bundle job
  - permissions grant ``packages: write`` (R1 publishing) and
    ``contents: write`` for the bundle release only — never outside
    the bundle job
  - R1: the concurrency group is keyed by the *resolved*
    component+version; the ghcr.io/kikakkz/looming/ registry prefix
    appears; the workflow_dispatch ``verify_only`` input exists; the
    immutable pre-check fails closed; ``resolve`` writes ``git_ref``
    and the checkout pins it
  - S2 (#172, AD-40): the publish job runs a VERSION consistency gate
    before the image build — the component's VERSION file content is
    read, validated as one vX.Y.Z line, and string-compared against
    the resolved tag version, exiting non-zero on mismatch with AD-40
    cited; the build receives the version as a docker build arg
    (S3 wires it into ldflags/OCI labels)
  - R3: the bundle resolve step rejects component-shaped tags, validates
    vX.Y.Z, and writes ``git_ref``; the bundle checkout pins that ref
  - R3: the pinned-image smoke gate pulls every pin, boots a postgres
    fixture, probes identityd (``/v1/self/login`` -> 400), topologyd
    (``/v1/internal/guide`` -> 401), and the gateway (-> 404), rechecks
    container liveness over ``docker ps -a`` (exited containers
    included — a post-probe crash is still red), and fails closed on a
    probe miss — a red gate means no Release (release-l1 §3.6)
  - R3: the CLI cross-compiles with CGO_ENABLED=0 for linux/amd64 +
    linux/arm64 from ./cmd/looming
  - R3: the release step runs ``gh release create --notes-file`` over
    the assets, with GH_TOKEN wired to secrets.GITHUB_TOKEN
  - R4: the asset set includes install.sh — the artifact-only installer
    (#151) ships in every bundle release, alongside the CLI binaries
    and the manifest

Usage: check_release_workflow.py [--workflow PATH]
Exit 0 when the contract holds, 1 listing what is missing.
"""

import argparse
import re
import sys
from pathlib import Path

EXPECTED_TAG_PATTERNS = ("gateway/v*.*.*", "identity/v*.*.*",
                         "topology/v*.*.*")
BUNDLE_TAG_PATTERN = "v*.*.*"
REGISTRY_PREFIX = "ghcr.io/kikakkz/looming/"
PACKAGES_WRITE_RE = re.compile(r"^\s*packages:\s*write\s*$", re.M)
CONTENTS_WRITE_RE = re.compile(r"^\s*contents:\s*write\s*$", re.M)
VERIFY_INPUT_RE = re.compile(r"^\s*verify_only:", re.M)
GIT_REF_WRITE_RE = re.compile(
    r"git_ref=refs/tags/\S+\s*>>\s*\"\$GITHUB_OUTPUT\"", re.M)
GIT_REF_JOB_OUTPUT_RE = re.compile(
    r"^\s*git_ref:\s*\$\{\{[^}]*\.outputs\.git_ref\s*\}\}\s*$", re.M)
CHECKOUT_TAG_REF = "ref: ${{ needs.resolve.outputs.git_ref }}"
PRECHECK_STEP = "Immutable pre-check"
NOT_FOUND_MARKERS = ("no such manifest", "manifest unknown", "not found",
                     "NAME_UNKNOWN")
# R3 (#161): the bundle-release contract (release-l1 §3.6/§5).
# R4 (#151): install.sh joins the release asset set.
DISJOINT_MARKER = 'never crosses a "/"'
COMPONENT_GUARD = ("if: ${{ github.event_name == 'workflow_dispatch' "
                   "|| contains(github.ref_name, '/') }}")
BUNDLE_GUARD = ("if: ${{ github.event_name == 'push' "
                "&& !contains(github.ref_name, '/') }}")
BUNDLE_JOB_RE = re.compile(r"^  bundle:", re.M)
BUNDLE_RESOLVE_STEP = "Resolve bundle version"
SLASH_GUARD = 'if [[ "$REF_NAME" == */* ]]; then'
VERSION_GUARD = 'if [[ ! "$REF_NAME" =~ ^v[0-9]+\\.[0-9]+\\.[0-9]+$ ]]; then'
BUNDLE_GIT_REF_RE = re.compile(
    r"git_ref=refs/tags/\$REF_NAME\"?\s*>>\s*\"\$GITHUB_OUTPUT\"")
BUNDLE_CHECKOUT_REF = "ref: ${{ steps.resolve.outputs.git_ref }}"
GATE_STEP = "Pinned-image smoke gate"
# S2 (#172, AD-40): the component's VERSION file is the canonical
# version source; the publish job gates on tag == file content and
# passes the version into the image build (S3 consumes the arg).
VERSION_GATE_STEP = "Check VERSION consistency"
VERSION_GATE_SHAPE_GUARD = (
    'if [[ ! "$file_version" =~ ^v[0-9]+\\.[0-9]+\\.[0-9]+$ ]]; then')
VERSION_GATE_COMPARE = '[[ "$file_version" != "$VERSION" ]]'
BUILD_STEP = "Build and push multi-arch image"
VERSION_BUILD_ARG = '--build-arg VERSION'
EXPECT_IDENTITY_RE = re.compile(r'expect\s+"[^"]*identity[^"]*"\s+400\b')
EXPECT_TOPOLOGY_RE = re.compile(r'expect\s+"[^"]*topology[^"]*"\s+401\b')
EXPECT_GATEWAY_RE = re.compile(r'expect\s+"[^"]*gateway[^"]*"\s+404\b')
IDENTITY_ROUTE = "/v1/self/login"
TOPOLOGY_ROUTE = "/v1/internal/guide"
CROSS_STEP = "Cross-compile CLI"
RELEASE_STEP = "Create GitHub Release"
RELEASE_ASSETS = ("looming-linux-amd64", "looming-linux-arm64",
                  "bundle-manifest.yaml", "install.sh")
# gh's repo-path#asset-name form: install.sh lives at scripts/install.sh
# in the checkout — a bare "install.sh" asset name names a file that
# does not exist at the workflow's workspace root and the release
# upload would fail (CodeRabbit #165 round 2).
REPO_PATH_ASSETS = {"install.sh": "scripts/install.sh#install.sh"}
GH_TOKEN_ENV = "GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}"


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


def _bundle_resolve_problems(text: str) -> list[str]:
    """The bundle job's resolve step must reject component-shaped tags,
    validate the bare vX.Y.Z shape, and pin the checkout to the tag."""
    bad = []
    script = _step_run(text, BUNDLE_RESOLVE_STEP)
    if script is None:
        return ["bundle version resolve step absent"]
    if SLASH_GUARD not in script:
        bad.append("bundle resolve does not reject component-shaped tags")
    if VERSION_GUARD not in script:
        bad.append("bundle resolve does not validate the vX.Y.Z shape")
    if not BUNDLE_GIT_REF_RE.search(script):
        bad.append("bundle resolve does not write `git_ref` to "
                   "$GITHUB_OUTPUT")
    if BUNDLE_CHECKOUT_REF not in text:
        bad.append("bundle checkout does not pin the resolved tag ref")
    return bad


def _version_gate_problems(text: str) -> list[str]:
    """The AD-40 consistency gate (S2, #172): before any image build,
    the component's VERSION file content must be read, validated as a
    single vX.Y.Z line (fail closed on anything else), and
    string-compared against the resolved tag version — a mismatch or a
    missing citation refuses the publish. The build also receives the
    version as a docker build arg, ready for S3's ldflags/OCI wiring."""
    bad = []
    script = _step_run(text, VERSION_GATE_STEP)
    if script is None:
        bad.append("VERSION consistency gate step absent (AD-40 S2, #172: "
                   "tag and VERSION file must agree before publishing)")
    else:
        if '"$COMPONENT/VERSION"' not in script:
            bad.append("VERSION gate does not read the component's "
                       "VERSION file")
        if VERSION_GATE_SHAPE_GUARD not in script:
            bad.append("VERSION gate does not fail closed on unparsable "
                       "file content (no vX.Y.Z shape guard)")
        if VERSION_GATE_COMPARE not in script:
            bad.append("VERSION gate does not string-compare the file "
                       "version against the resolved tag version")
        if "AD-40" not in script:
            bad.append("VERSION gate error message does not cite AD-40")
        if "exit 1" not in script:
            bad.append("VERSION gate does not exit non-zero on mismatch")
        build_pos = text.find(f"- name: {BUILD_STEP}")
        gate_pos = text.find(f"- name: {VERSION_GATE_STEP}")
        if build_pos != -1 and gate_pos > build_pos:
            bad.append("VERSION gate must run before the image build "
                       "(reject before any push)")
    build = _step_run(text, BUILD_STEP)
    if build is None:
        bad.append("component build step absent")
    elif VERSION_BUILD_ARG not in build:
        bad.append("component build does not pass the VERSION content "
                   "as a docker build arg (S3 prep)")
    return bad


def _smoke_gate_problems(text: str) -> list[str]:
    """The pinned-image gate must pull every pin, boot against a
    postgres fixture, probe each component's liveness surface, recheck
    container liveness, and fail closed on a miss (release-l1 §3.6:
    a red gate means no Release)."""
    bad = []
    script = _step_run(text, GATE_STEP)
    if script is None:
        return ["pinned-image smoke gate step absent (release-l1 §3.6: "
                "publication gates on the gate)"]
    if "docker pull" not in script:
        bad.append("smoke gate does not pull the pinned images")
    if "pg_isready" not in script:
        bad.append("smoke gate has no postgres fixture (pg_isready)")
    if not EXPECT_IDENTITY_RE.search(script) or IDENTITY_ROUTE not in script:
        bad.append(f"smoke gate lacks the identityd probe "
                   f"({IDENTITY_ROUTE} -> 400)")
    if not EXPECT_TOPOLOGY_RE.search(script) or TOPOLOGY_ROUTE not in script:
        bad.append(f"smoke gate lacks the topologyd probe "
                   f"({TOPOLOGY_ROUTE} -> 401)")
    if not EXPECT_GATEWAY_RE.search(script):
        bad.append("smoke gate lacks the gateway probe (-> 404)")
    if "docker ps -a" not in script:
        bad.append("smoke gate's liveness recheck cannot see exited "
                   "containers (`docker ps -a` missing — plain `docker "
                   "ps` would skip a crashed container)")
    if "docker inspect" not in script or "State.Running" not in script:
        bad.append("smoke gate has no post-probe liveness recheck")
    if "never answered" not in script or "exit 1" not in script:
        bad.append("smoke gate does not fail closed on a probe miss")
    return bad


def _cross_compile_problems(text: str) -> list[str]:
    """The CLI must cross-compile from source with CGO disabled for
    exactly the linux/amd64 + linux/arm64 asset pair."""
    script = _step_run(text, CROSS_STEP)
    if script is None:
        return ["CLI cross-compile step absent"]
    bad = []
    if "CGO_ENABLED=0" not in script:
        bad.append("CLI cross-compile does not set CGO_ENABLED=0")
    if "GOARCH=amd64" not in script or "GOARCH=arm64" not in script:
        bad.append("CLI cross-compile must build linux/amd64 + "
                   "linux/arm64")
    if "./cmd/looming" not in script:
        bad.append("CLI cross-compile does not build ./cmd/looming")
    return bad


def _strip_shell_comment(line: str) -> str:
    """Drop a trailing shell comment — a '#' preceded by whitespace and
    sitting outside quotes (full-comment lines therefore strip to
    blank). Deterministic by design for the workflow-run-block domain
    this checker pins: no heredocs, no quote-escape games."""
    in_squote = in_dquote = False
    for i, ch in enumerate(line):
        if ch == "'" and not in_dquote:
            in_squote = not in_squote
        elif ch == '"' and not in_squote:
            in_dquote = not in_dquote
        elif ch == "#" and not in_squote and not in_dquote:
            if i == 0 or line[i - 1] in " \t":
                return line[:i]
    return line


def _release_command_assets(script: str) -> list[str] | None:
    """The whitespace-separated tokens of the ``gh release create``
    command — the only place an asset name is real. Comment lines and
    trailing comments are stripped first (a commented-out release
    command must not satisfy the check), and continuation lines
    (backslash) join into one logical command. None when the step never
    invokes it.

    Asset checks must parse THIS line, not the step body: a comment or
    a neighboring command carrying the right-looking text would
    otherwise satisfy the check while the actual upload used a bare
    name (CodeRabbit #165 rounds 3–4)."""
    lines = script.splitlines()
    for i, line in enumerate(lines):
        line = _strip_shell_comment(line)
        if not line.strip():
            continue
        if "gh release create" not in line:
            continue
        cmd = line
        while cmd.rstrip().endswith("\\") and i + 1 < len(lines):
            nxt = _strip_shell_comment(lines[i + 1])
            cmd = cmd.rstrip()[:-1] + " " + nxt.strip()
            i += 1
        return cmd.split()
    return None


def _uploads_asset(tokens: list[str], asset: str) -> bool:
    """True when the command uploads `asset`: either as the exact token
    or via gh's repo-path#asset-name rename form (…#<asset>)."""
    return any(t == asset or t.endswith("#" + asset) for t in tokens)


def _release_step_problems(text: str) -> list[str]:
    """The release step must create the GH Release with generated notes
    over the full asset set: the two CLI binaries, the bundle manifest,
    and install.sh — the artifact-only installer rides every bundle
    release (release-l1 §2, R4 #151). install.sh uploads via gh's
    repo-path#asset-name form (it lives at scripts/install.sh in the
    checkout; a bare asset name names a nonexistent root file). Asset
    names are checked as tokens of the gh release create command line
    itself, never as free text anywhere in the step."""
    bad = []
    script = _step_run(text, RELEASE_STEP)
    if script is None:
        return ["release step absent (`- name: Create GitHub Release` "
                "missing)"]
    tokens = _release_command_assets(script)
    if tokens is None:
        bad.append("release step does not run `gh release create`")
        return bad
    if "--notes-file" not in tokens:
        bad.append("release step does not attach the generated notes "
                   "(--notes-file missing)")
    absent = [a for a in RELEASE_ASSETS if not _uploads_asset(tokens, a)]
    if absent:
        bad.append("release assets incomplete (missing: "
                   + ", ".join(absent) + ")")
    for asset, repo_form in REPO_PATH_ASSETS.items():
        if repo_form not in tokens:
            bad.append(f"release asset {asset} must upload from its "
                       f"repo path ('{repo_form}' — gh's "
                       f"repo-path#asset-name form; a bare '{asset}' "
                       f"does not exist at the checkout root and the "
                       f"release upload would fail)")
    return bad


def _job_first_property(text: str, job: str) -> str | None:
    """The first property line of a job (after ``  <job>:``), skipping
    comments and blanks — where a job-level ``if:`` guard must sit for
    it to unambiguously belong to that job."""
    lines = text.splitlines()
    for i, line in enumerate(lines):
        if line == f"  {job}:":
            for cand in lines[i + 1:]:
                stripped = cand.strip()
                if not stripped or stripped.startswith("#"):
                    continue
                return stripped
            return None
    return None


def _bundle_problems(text: str) -> list[str]:
    """The R3 bundle-release contract (release-l1 §3.6/§5, #161)."""
    bad = []
    if DISJOINT_MARKER not in text:
        bad.append("tag-disjointness note missing (glob `*` never "
                   "crosses `/`, so 'v*.*.*' cannot match component tags)")
    if COMPONENT_GUARD not in text:
        bad.append("component jobs do not declare the tag-shape guard "
                   "(workflow_dispatch or a tag containing '/')")
    elif _job_first_property(text, "resolve") != COMPONENT_GUARD:
        bad.append("component jobs' guard is not the resolve job's first "
                   "property — it does not guard the component jobs")
    bundle_pos = text.find(BUNDLE_GUARD)
    job_match = BUNDLE_JOB_RE.search(text)
    if bundle_pos == -1:
        bad.append("bundle job does not declare its bare-tag guard "
                   "(push and no '/' in the ref)")
    elif job_match and (bundle_pos < job_match.start()
                        or _job_first_property(text, "bundle") != BUNDLE_GUARD):
        bad.append("bundle job's bare-tag guard is not the bundle job's "
                   "first property — it guards nothing")
    writes = list(CONTENTS_WRITE_RE.finditer(text))
    if not writes:
        bad.append("permissions do not grant `contents: write` for the "
                   "bundle release")
    elif job_match and any(w.start() < job_match.start() for w in writes):
        bad.append("`contents: write` is granted outside the bundle job")
    bad += _bundle_resolve_problems(text)
    bad += _smoke_gate_problems(text)
    bad += _cross_compile_problems(text)
    bad += _release_step_problems(text)
    if GH_TOKEN_ENV not in text:
        bad.append("GH_TOKEN is not wired to secrets.GITHUB_TOKEN")
    if "git log" not in text or "--pretty=%s" not in text:
        bad.append("release notes are not generated from conventional "
                   "commits (`git log --pretty=%s` missing)")
    return bad


def problems(text: str) -> list[str]:
    """Return the list of R1+R3 contract pieces missing from the workflow."""
    missing = []
    active = _active_push_tags(text)
    for pattern in EXPECTED_TAG_PATTERNS:
        if pattern not in active:
            missing.append(
                f"active on.push.tags entry '{pattern}' is not declared")
    if BUNDLE_TAG_PATTERN not in active:
        missing.append(
            f"bundle tag pattern '{BUNDLE_TAG_PATTERN}' is not declared")
    allowed = EXPECTED_TAG_PATTERNS + (BUNDLE_TAG_PATTERN,)
    for entry in active:
        if entry not in allowed:
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
    resolve_script = _step_run(text, "Resolve component and version")
    if resolve_script is None or not GIT_REF_WRITE_RE.search(resolve_script):
        missing.append("resolve does not write `git_ref` to $GITHUB_OUTPUT")
    if not GIT_REF_JOB_OUTPUT_RE.search(text):
        missing.append("resolve job outputs do not map `git_ref`")
    if CHECKOUT_TAG_REF not in text:
        missing.append("checkout does not pin the resolved tag ref")
    missing += _version_gate_problems(text)
    missing += _bundle_problems(text)
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
