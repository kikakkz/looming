#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Smoke tests for scripts/release.sh (#166) against throwaway git repos.

The driver is exercised through its non-interactive + --dry-run surface
(the same vehicle install.sh uses for its shell assertions): the script
and its two inputs (bundle/manifest.yaml, .ai/tools/release_version.py)
are copied into a fixture repository, tags are planted there, and every
run asserts the printed plan while proving zero side effects — no tag
is ever created and nothing is ever pushed.
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

    def run_script(self, *args: str, stdin=None, env=None) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["bash", str(self.repo / "scripts" / "release.sh"), *args],
            cwd=self.repo, env=env or self.env, capture_output=True, text=True,
            stdin=stdin,
        )

    def tags(self) -> list[str]:
        return sorted(self.git("tag", "-l").splitlines())

    def test_dry_run_patch_prints_plan_without_side_effects(self):
        before = self.tags()
        r = self.run_script("gateway", "patch", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        # max(gateway tags) is v0.1.1 -> patch v0.1.2.
        self.assertIn("gateway/v0.1.2", r.stdout)
        self.assertIn("dry-run", r.stdout.lower())
        self.assertIn("gh run list --workflow=release.yml", r.stdout)
        self.assertEqual(self.tags(), before, "dry-run must not create tags")

    def test_dry_run_minor(self):
        r = self.run_script("gateway", "minor", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("gateway/v0.2.0", r.stdout)

    def test_dry_run_never_released_component_starts_at_v0_1_0(self):
        r = self.run_script("topology", "patch", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("topology/v0.1.0", r.stdout)
        self.assertIn("initial", r.stdout)

    def test_component_versions_are_isolated(self):
        # identity's v0.2.0 must not leak into gateway's derivation.
        r = self.run_script("gateway", "patch", "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("gateway/v0.1.2", r.stdout)

    def test_unknown_component_rejected(self):
        r = self.run_script("nosuch", "patch", "--dry-run")
        self.assertEqual(r.returncode, 1)
        self.assertIn("nosuch", r.stderr)

    def test_unknown_bump_rejected(self):
        r = self.run_script("gateway", "major", "--dry-run")
        self.assertNotEqual(r.returncode, 0)

    def test_help_exits_zero(self):
        r = self.run_script("--help")
        self.assertEqual(r.returncode, 0)
        self.assertIn("Usage:", r.stdout)

    def test_no_args_without_tty_prints_usage_and_exits_2(self):
        r = self.run_script(stdin=subprocess.DEVNULL)
        self.assertEqual(r.returncode, 2)
        self.assertIn("Usage:", r.stderr)

    def test_refuses_when_tag_exists_on_remote(self):
        # The computed tag existing on the remote (but not in this
        # clone) must refuse — closing the "everything up to date"
        # false-success hole. A successful refresh would import the
        # remote tag and move the math past it, so the fixture no-ops
        # `git fetch` via a PATH shim: the ls-remote guard alone must
        # catch the collision (the offline/stale-fetch window).
        remote = self.tmp / "remote.git"
        subprocess.run(["git", "init", "-q", "--bare", str(remote)],
                       check=True, capture_output=True)
        self.git("tag", "gateway/v0.1.2")
        self.git("remote", "add", "origin", str(remote))
        self.git("push", "-q", "origin", "gateway/v0.1.2")
        self.git("tag", "-d", "gateway/v0.1.2")
        shim = self.tmp / "shim"
        shim.mkdir()
        (shim / "git").write_text(
            '#!/bin/sh\n[ "$1" = "fetch" ] && exit 0\n'
            'exec /usr/bin/git "$@"\n', encoding="utf-8")
        os.chmod(shim / "git", 0o755)
        env = dict(self.env, PATH=f"{shim}{os.pathsep}{os.environ['PATH']}")
        r = self.run_script("gateway", "patch", "--dry-run", env=env)
        self.assertEqual(r.returncode, 1)
        self.assertIn("already exists on origin", r.stderr)


@unittest.skipUnless(shutil.which("script"), "util-linux script(1) unavailable")
class InteractiveMenuTests(unittest.TestCase):
    """The TTY menu: list components, pick bump, decline the confirm."""

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.repo, self.env = make_fixture(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_menu_lists_components_and_aborts_on_no(self):
        script_bin = shutil.which("script")
        r = subprocess.run(
            [script_bin, "-qec",
             f"bash {self.repo}/scripts/release.sh", "/dev/null"],
            cwd=self.repo, env=self.env, input="1\n1\nn\n",
            capture_output=True, text=True,
        )
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("gateway", r.stdout)
        self.assertIn("identity", r.stdout)
        self.assertIn("topology", r.stdout)
        self.assertIn("gateway/v0.1.2", r.stdout)  # patch candidate
        self.assertIn("gateway/v0.2.0", r.stdout)  # minor candidate
        self.assertIn("aborted", r.stdout)
        tags = subprocess.run(["git", "tag", "-l"], cwd=self.repo,
                              env=self.env, capture_output=True, text=True,
                              check=True).stdout.splitlines()
        self.assertNotIn("gateway/v0.1.2", tags)


if __name__ == "__main__":
    unittest.main()
