#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Smoke tests for scripts/release.sh (#166, AD-40 three-step flow #172).

The driver is exercised through its two non-interactive surfaces (the
same vehicle install.sh uses for its shell assertions): `--list` (the
machine-readable candidate feed for AI agents' menus) and the
`<component> <bump> [--dry-run]` execution form walking the AD-40
three-step flow (bump-VERSION PR → merge → tag). The script and its two
inputs (bundle/manifest.yaml, .ai/tools/release_version.py) are copied
into a fixture repository whose components carry VERSION files (the
canonical source since #172), and every run asserts its output while
proving the promised side effects — dry runs and --list are read-only;
step-1 runs push only the bump branch; step-2 runs push only the tag.
"""

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent.parent
SCRIPT = REPO_ROOT / "scripts" / "release.sh"
MANIFEST = REPO_ROOT / "bundle" / "manifest.yaml"
VERSION_TOOL = REPO_ROOT / ".ai" / "tools" / "release_version.py"

GATEWAY_VERSION = """\
# SPDX-License-Identifier: Apache-2.0

# Canonical component version (AD-40): header comments the parser must
# skip — the single line below is the whole content.
v0.1.1
"""


def make_fixture(tmp: Path) -> tuple[Path, dict]:
    """A throwaway repo carrying the driver and its two inputs."""
    repo = tmp / "repo"
    (repo / "bundle").mkdir(parents=True)
    (repo / ".ai" / "tools").mkdir(parents=True)
    (repo / "scripts").mkdir()
    shutil.copy2(SCRIPT, repo / "scripts" / "release.sh")
    shutil.copy2(MANIFEST, repo / "bundle" / "manifest.yaml")
    shutil.copy2(VERSION_TOOL, repo / ".ai" / "tools" / "release_version.py")
    env = os.environ.copy()
    env["GIT_CONFIG_GLOBAL"] = "/dev/null"
    env["GIT_CONFIG_SYSTEM"] = "/dev/null"

    def git(*args: str) -> str:
        r = subprocess.run(["git", *args], cwd=repo, env=env,
                           check=True, capture_output=True, text=True)
        return r.stdout

    git("init", "-q", "-b", "main")
    git("config", "user.name", "T")
    git("config", "user.email", "t@example.com")
    (repo / "f.txt").write_text("x", encoding="utf-8")
    git("add", "-A")
    git("commit", "-q", "-m", "init")
    # The AD-40 source of truth: component VERSION files. topology ships
    # none yet — the unreleased component. The tags below exist only to
    # model "released" states for derivation; since #172 they never
    # drive version math.
    (repo / "gateway").mkdir()
    (repo / "gateway" / "VERSION").write_text(GATEWAY_VERSION,
                                              encoding="utf-8")
    (repo / "identity").mkdir()
    (repo / "identity" / "VERSION").write_text("v0.2.0\n", encoding="utf-8")
    for tag in ("gateway/v0.1.0", "gateway/v0.1.1", "identity/v0.2.0"):
        git("tag", tag)
    return repo, env


class ReleaseShTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.repo, self.env = make_fixture(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def git(self, *args: str) -> str:
        r = subprocess.run(["git", *args], cwd=self.repo, env=self.env,
                           check=True, capture_output=True, text=True)
        return r.stdout

    def run_script(self, *args: str, env=None) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["bash", str(self.repo / "scripts" / "release.sh"), *args],
            cwd=self.repo, env=env or self.env, capture_output=True, text=True,
        )

    def tags(self) -> list[str]:
        return sorted(self.git("tag", "-l").splitlines())

    def heads(self, remote: Path) -> list[str]:
        r = subprocess.run(["git", "ls-remote", "--heads", str(remote)],
                           env=self.env, check=True, capture_output=True,
                           text=True)
        return sorted(line.split("\t")[1].removeprefix("refs/heads/")
                      for line in r.stdout.splitlines() if "\t" in line)

    def bare_remote(self) -> Path:
        remote = self.tmp / "remote.git"
        subprocess.run(["git", "init", "-q", "--bare", str(remote)],
                       check=True, capture_output=True)
        self.git("remote", "add", "origin", str(remote))
        return remote

    def test_dry_run_patch_prints_three_step_plan_without_side_effects(self):
        before = self.tags()
        r = self.run_script("gateway", "patch", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        # The file says v0.1.1 and its tag exists (released): patch
        # targets v0.1.2.
        self.assertIn("gateway/v0.1.2", r.stdout)
        self.assertIn("step 1", r.stdout)
        self.assertIn("step 2", r.stdout)
        self.assertIn("step 3", r.stdout)
        self.assertIn("chore: bump gateway VERSION to v0.1.2 (#168)",
                      r.stdout)
        self.assertIn("gh run list --workflow=release.yml", r.stdout)
        self.assertIn("dry-run", r.stdout.lower())
        self.assertEqual(self.tags(), before, "dry-run must not create tags")

    def test_dry_run_minor(self):
        r = self.run_script("gateway", "minor", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("gateway/v0.2.0", r.stdout)

    def test_dry_run_major_zeroes_minor_and_patch(self):
        r = self.run_script("gateway", "major", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("gateway/v1.0.0", r.stdout)

    def test_dry_run_never_released_component_starts_at_v0_1_0(self):
        # topology has no VERSION file: the initial release creates it.
        r = self.run_script("topology", "patch", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("topology/v0.1.0", r.stdout)
        self.assertIn("initial", r.stdout)

    def test_dry_run_pending_release_bumps_nothing(self):
        # A VERSION version with no tag yet IS the pending release: the
        # file already names the target, so no bump happens.
        self.git("tag", "-d", "gateway/v0.1.1")
        (self.repo / "gateway" / "VERSION").write_text("v0.3.0\n",
                                                       encoding="utf-8")
        r = self.run_script("gateway", "patch", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("gateway/v0.3.0", r.stdout)
        self.assertIn("no bump", r.stdout)

    def test_component_versions_are_isolated(self):
        # identity's v0.2.0 must not leak into gateway's derivation.
        (self.repo / "identity" / "VERSION").write_text("v9.9.9\n",
                                                        encoding="utf-8")
        r = self.run_script("gateway", "patch", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("gateway/v0.1.2", r.stdout)

    def test_tags_do_not_drive_derivation(self):
        # Since #172 the file is the source: a higher stray tag must not
        # move the math past the file's version.
        self.git("tag", "gateway/v9.9.9")
        r = self.run_script("gateway", "patch", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("gateway/v0.1.2", r.stdout)
        r = self.run_script("--list")
        self.assertEqual(r.returncode, 0, r.stderr)
        by_component = {line.split("\t")[0]: line.split("\t")[1:]
                        for line in r.stdout.splitlines() if line.strip()}
        self.assertEqual(by_component["gateway"][0], "v0.1.1")

    def test_bad_version_file_fails_loud(self):
        (self.repo / "gateway" / "VERSION").write_text("banana\n",
                                                       encoding="utf-8")
        r = self.run_script("gateway", "patch", "--dry-run")
        self.assertEqual(r.returncode, 1)
        self.assertIn("VERSION", r.stderr)

    def test_unknown_component_rejected(self):
        r = self.run_script("nosuch", "patch", "--dry-run")
        self.assertEqual(r.returncode, 1)
        self.assertIn("nosuch", r.stderr)

    def test_unknown_bump_rejected(self):
        r = self.run_script("gateway", "micro", "--dry-run")
        self.assertNotEqual(r.returncode, 0)

    def test_help_exits_zero(self):
        r = self.run_script("--help")
        self.assertEqual(r.returncode, 0)
        self.assertIn("Usage:", r.stdout)
        self.assertIn("three", r.stdout.lower())

    def test_no_args_prints_usage_and_exits_2(self):
        r = self.run_script()
        self.assertEqual(r.returncode, 2)
        self.assertIn("Usage:", r.stderr)
        self.assertIn("--list", r.stderr)

    def test_refuses_when_tag_exists_on_remote(self):
        # The target tag existing on the remote (but not in this clone)
        # must refuse — closing the "everything up to date" false-success
        # hole for an immutable published tag.
        remote = self.bare_remote()
        self.git("tag", "gateway/v0.1.2")
        self.git("push", "-q", "origin", "gateway/v0.1.2")
        self.git("tag", "-d", "gateway/v0.1.2")
        r = self.run_script("gateway", "patch", "--dry-run")
        self.assertEqual(r.returncode, 1)
        self.assertIn("already exists on origin", r.stderr)
        self.assertEqual(self.heads(remote), [])

    def test_ls_remote_failure_fails_closed(self):
        # Only ls-remote exit 2 means "no such tag". A network/auth
        # failure (any other non-zero) must abort, not proceed.
        self.bare_remote()
        env = dict(self.env, PATH=self.shim_path(fail="ls-remote"))
        r = self.run_script("gateway", "patch", "--dry-run", env=env)
        self.assertEqual(r.returncode, 1)
        self.assertIn("failing closed", r.stderr)
        self.assertNotIn("gateway/v0.1.2", self.tags(),
                         "no tag on a failed check")

    def test_never_fetches(self):
        # The VERSION file replaced tags as the source — no surface ever
        # refreshes refs: --list works on a tagless, offline clone.
        self.bare_remote()
        marker = self.tmp / "fetch-marker"
        env = dict(self.env, PATH=self.fetch_count_path(marker))
        r = self.run_script("gateway", "patch", "--dry-run", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertFalse(marker.exists(), "dry-run must not fetch")
        r = self.run_script("--list", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertFalse(marker.exists(), "--list must not fetch")

    def test_step1_pushes_bump_branch_and_opens_pr(self):
        # Full step-1 execution against a local bare remote with a
        # scripted gh: branch pushed, PR created, original branch
        # restored, no tag cut.
        remote = self.bare_remote()
        self.git("push", "-q", "origin", "main")
        args_log = self.tmp / "gh-args.log"
        env = dict(self.env, PATH=self.gh_path(args_log))
        r = self.run_script("gateway", "patch", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("step 1", r.stdout)
        self.assertEqual(self.heads(remote),
                         ["chore/bump-gateway-v0.1.2", "main"])
        self.assertEqual(self.git("branch", "--show-current").strip(), "main")
        # The pushed branch carries the bumped file.
        self.git("fetch", "-q", "origin", "chore/bump-gateway-v0.1.2")
        out = self.git("show", "FETCH_HEAD:gateway/VERSION")
        self.assertEqual(out.strip().splitlines()[-1], "v0.1.2")
        # The PR was asked for with the AD-40 title and issue ref.
        logged = args_log.read_text(encoding="utf-8")
        self.assertIn("chore: bump gateway VERSION to v0.1.2 (#168)", logged)
        # Step 2 has not happened: no release tag exists anywhere.
        self.assertNotIn("gateway/v0.1.2", self.tags())
        r2 = subprocess.run(
            ["git", "ls-remote", "--tags", str(remote)], env=self.env,
            check=True, capture_output=True, text=True)
        self.assertNotIn("gateway/v0.1.2", r2.stdout)

    def test_step1_gh_failure_aborts_with_guidance(self):
        remote = self.bare_remote()
        self.git("push", "-q", "origin", "main")
        env = dict(self.env, PATH=self.gh_failing_path())
        r = self.run_script("gateway", "patch", env=env)
        self.assertEqual(r.returncode, 1)
        self.assertIn("gh pr create failed", r.stderr)
        # The branch is pushed — the PR can be opened manually.
        self.assertEqual(self.heads(remote),
                         ["chore/bump-gateway-v0.1.2", "main"])

    def test_step2_tags_main_when_bump_merged(self):
        # Re-running after the bump PR merged: origin/main carries the
        # new VERSION, so the flow detects step 1 done and cuts the tag
        # at origin/main — nothing else moves.
        self.bare_remote()
        self.git("push", "-q", "origin", "main")
        # Simulate the squash-merged bump PR on main.
        (self.repo / "gateway" / "VERSION").write_text("v0.1.2\n",
                                                       encoding="utf-8")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "chore: bump gateway VERSION to v0.1.2")
        self.git("push", "-q", "origin", "main")
        r = self.run_script("gateway", "patch")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("step 2", r.stdout)
        self.assertIn("step 3", r.stdout)
        self.assertIn("gateway/v0.1.2", self.tags())
        r2 = subprocess.run(
            ["git", "ls-remote", "--tags", str(self.tmp / "remote.git")],
            env=self.env, check=True, capture_output=True, text=True)
        self.assertIn("gateway/v0.1.2", r2.stdout)

    def test_execution_requires_origin(self):
        r = self.run_script("gateway", "patch")
        self.assertEqual(r.returncode, 1)
        self.assertIn("origin", r.stderr)

    def shim_path(self, fail: str) -> str:
        """A PATH whose `git` intercepts one subcommand — ls-remote
        simulates a network failure (rc 128) — and delegates everything
        else to the real git binary, resolved dynamically."""
        real_git = shutil.which("git")
        self.assertIsNotNone(real_git)
        intercept = (f'[ "$1" = "{fail}" ] && {{ '
                     f'echo "simulated {fail} failure" >&2; exit 128; }}\n')
        shim = self.tmp / f"shim-{fail}"
        shim.mkdir()
        (shim / "git").write_text(
            f'#!/bin/sh\n{intercept}exec "{real_git}" "$@"\n',
            encoding="utf-8")
        os.chmod(shim / "git", 0o755)
        return f"{shim}{os.pathsep}{os.environ['PATH']}"

    def fetch_count_path(self, marker: Path) -> str:
        """A PATH whose `git` records every fetch, then delegates."""
        real_git = shutil.which("git")
        self.assertIsNotNone(real_git)
        shim = self.tmp / "shim-fetch-count"
        shim.mkdir()
        (shim / "git").write_text(
            f'#!/bin/sh\nif [ "$1" = "fetch" ]; then touch "{marker}"; fi\n'
            f'exec "{real_git}" "$@"\n', encoding="utf-8")
        os.chmod(shim / "git", 0o755)
        return f"{shim}{os.pathsep}{os.environ['PATH']}"

    def gh_path(self, args_log: Path) -> str:
        """A PATH whose `gh` answers the driver's two calls (no PR yet;
        create succeeds) and logs every argument."""
        real_gh = shutil.which("gh")
        self.assertIsNotNone(real_gh)
        shim = self.tmp / "shim-gh"
        shim.mkdir()
        (shim / "gh").write_text(
            '#!/bin/sh\n'
            f'printf \'%s\\n\' "$@" >> "{args_log}"\n'
            'if [ "$1" = "pr" ] && [ "$2" = "view" ]; then exit 1; fi\n'
            'if [ "$1" = "pr" ] && [ "$2" = "create" ]; then\n'
            '  echo "https://example.invalid/kikakkz/looming/pull/1"\n'
            '  exit 0\n'
            'fi\n'
            'echo "unexpected gh $*" >&2\n'
            'exit 1\n', encoding="utf-8")
        os.chmod(shim / "gh", 0o755)
        return f"{shim}{os.pathsep}{os.environ['PATH']}"

    def gh_failing_path(self) -> str:
        """A PATH whose `gh` fails every call (auth/network outage)."""
        real_gh = shutil.which("gh")
        self.assertIsNotNone(real_gh)
        shim = self.tmp / "shim-gh-fail"
        shim.mkdir()
        (shim / "gh").write_text(
            '#!/bin/sh\necho "simulated gh failure" >&2\nexit 1\n',
            encoding="utf-8")
        os.chmod(shim / "gh", 0o755)
        return f"{shim}{os.pathsep}{os.environ['PATH']}"


class ListModeTests(unittest.TestCase):
    """--list: the machine-readable candidate feed for agent menus."""

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.repo, self.env = make_fixture(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def run_script(self, *args: str, env=None) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["bash", str(self.repo / "scripts" / "release.sh"), *args],
            cwd=self.repo, env=env or self.env, capture_output=True, text=True,
        )

    def test_list_emits_one_tsv_line_per_component(self):
        r = self.run_script("--list")
        self.assertEqual(r.returncode, 0, r.stderr)
        lines = [line for line in r.stdout.splitlines() if line.strip()]
        self.assertEqual(len(lines), 3)
        by_component = {}
        for line in lines:
            fields = line.split("\t")
            self.assertEqual(len(fields), 5, f"not 5 TSV fields: {line!r}")
            by_component[fields[0]] = fields[1:]
        # gateway's file says v0.1.1 (and its tag marks it released).
        self.assertEqual(by_component["gateway"],
                         ["v0.1.1", "v0.1.2", "v0.2.0", "v1.0.0"])
        self.assertEqual(by_component["identity"],
                         ["v0.2.0", "v0.2.1", "v0.3.0", "v1.0.0"])
        # No VERSION file: the none marker and all three initial
        # candidates, with an unreleased note on stderr.
        self.assertEqual(by_component["topology"],
                         ["none", "v0.1.0", "v0.1.0", "v0.1.0"])
        self.assertIn("topology", r.stderr)
        self.assertIn("unreleased", r.stderr)

    def test_list_is_read_only(self):
        before = subprocess.run(["git", "status", "--porcelain"],
                                cwd=self.repo, env=self.env,
                                capture_output=True, text=True,
                                check=True).stdout
        r = self.run_script("--list")
        self.assertEqual(r.returncode, 0, r.stderr)
        after = subprocess.run(["git", "status", "--porcelain"],
                               cwd=self.repo, env=self.env,
                               capture_output=True, text=True,
                               check=True).stdout
        self.assertEqual(before, after)

    def test_list_works_without_origin_and_tags(self):
        # A tagless, remoteless clone still gets candidates — the files
        # are the source (#172 acceptance).
        for tag in ("gateway/v0.1.0", "gateway/v0.1.1", "identity/v0.2.0"):
            subprocess.run(["git", "tag", "-d", tag], cwd=self.repo,
                           env=self.env, check=True, capture_output=True)
        r = self.run_script("--list")
        self.assertEqual(r.returncode, 0, r.stderr)
        by_component = {line.split("\t")[0]: line.split("\t")[1:]
                        for line in r.stdout.splitlines() if line.strip()}
        self.assertEqual(by_component["gateway"][1], "v0.1.2")

    def test_list_rejects_operational_args(self):
        r = self.run_script("--list", "gateway")
        self.assertEqual(r.returncode, 2)
        self.assertIn("Usage:", r.stderr)

    def test_list_bad_version_file_fails(self):
        (self.repo / "gateway" / "VERSION").write_text("v2.1.0-beta\n",
                                                       encoding="utf-8")
        r = self.run_script("--list")
        self.assertEqual(r.returncode, 1)
        self.assertIn("VERSION", r.stderr)


if __name__ == "__main__":
    unittest.main()
