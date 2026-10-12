#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for release_version.py (#166)."""

import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import release_version

SCRIPT = Path(__file__).resolve().parent.parent / "release_version.py"


class NextVersionTests(unittest.TestCase):
    def test_no_tags_initial_version(self):
        for bump in ("patch", "minor"):
            with self.subTest(bump=bump):
                self.assertEqual(release_version.next_version([], bump), "v0.1.0")

    def test_patch_bump(self):
        tags = ["gateway/v0.1.0"]
        self.assertEqual(release_version.next_version(tags, "patch"), "v0.1.1")

    def test_minor_bump_resets_patch(self):
        tags = ["gateway/v0.1.7"]
        self.assertEqual(release_version.next_version(tags, "minor"), "v0.2.0")

    def test_max_semver_wins_not_lexicographic(self):
        # v0.1.10 > v0.1.9 numerically but not as strings; the max must
        # come from numeric field ordering or the patch lands on .9+1.
        tags = ["gateway/v0.1.9", "gateway/v0.1.10", "gateway/v0.1.2"]
        self.assertEqual(release_version.next_version(tags, "patch"), "v0.1.11")
        self.assertEqual(release_version.next_version(tags, "minor"), "v0.2.0")

    def test_cross_digit_patch(self):
        self.assertEqual(
            release_version.next_version(["gateway/v0.9.9"], "patch"), "v0.9.10")

    def test_cross_digit_minor(self):
        self.assertEqual(
            release_version.next_version(["gateway/v0.9.9"], "minor"), "v0.10.0")

    def test_multi_digit_major_compare(self):
        tags = ["gateway/v9.0.0", "gateway/v10.0.0"]
        self.assertEqual(release_version.next_version(tags, "patch"), "v10.0.1")

    def test_ignores_invalid_tags(self):
        tags = [
            "gateway/v0.1.0",
            "not-a-version",
            "gateway/v1.0",              # two fields only
            "gateway/v1.0.0-beta",       # semver pre-release is not a release
            "gateway/vX.Y.Z",
            "v0.3.0",                    # bare bundle tag, no component prefix
            "",
        ]
        self.assertEqual(release_version.next_version(tags, "patch"), "v0.1.1")

    def test_no_valid_version_raises(self):
        # Non-empty input with zero valid tags is a caller/pattern fault,
        # not an initial release — fail closed instead of minting v0.1.0.
        with self.assertRaises(ValueError):
            release_version.next_version(["junk", "gateway/v1.0"], "patch")

    def test_no_valid_version_raises_for_generator(self):
        # One-shot iterators must hit the same fail-closed branch — the
        # parse loop exhausts them, so the emptiness check re-reads a
        # materialized copy.
        with self.assertRaises(ValueError):
            release_version.next_version(iter(["junk"]), "patch")

    def test_unknown_bump_raises(self):
        with self.assertRaises(ValueError):
            release_version.next_version(["gateway/v0.1.0"], "major")

    def test_generator_input_accepted(self):
        self.assertEqual(
            release_version.next_version(iter(["gateway/v2.3.4"]), "minor"),
            "v2.4.0",
        )


class CliTests(unittest.TestCase):
    """The --component/--bump entry reads tags from the cwd's git repo."""

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.repo = self.tmp / "repo"
        self.repo.mkdir()
        self.env = os.environ.copy()
        self.env["GIT_CONFIG_GLOBAL"] = "/dev/null"
        self.env["GIT_CONFIG_SYSTEM"] = "/dev/null"
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.name", "T")
        self.git("config", "user.email", "t@example.com")

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def git(self, *args: str) -> None:
        subprocess.run(["git", *args], cwd=self.repo, env=self.env,
                       check=True, capture_output=True)

    def run_cli(self, *args: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            cwd=self.repo, env=self.env, capture_output=True, text=True,
        )

    def commit(self) -> None:
        (self.repo / "f.txt").write_text("x", encoding="utf-8")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "init")

    def test_patch_from_tag(self):
        self.commit()
        self.git("tag", "gateway/v0.1.0")
        r = self.run_cli("--component", "gateway", "--bump", "patch")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.1")

    def test_minor_from_tag(self):
        self.commit()
        self.git("tag", "gateway/v0.9.9")
        r = self.run_cli("--component", "gateway", "--bump", "minor")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.10.0")

    def test_no_tags_initial_version(self):
        self.commit()
        r = self.run_cli("--component", "topology", "--bump", "patch")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.0")

    def test_component_filter_isolates_versions(self):
        self.commit()
        self.git("tag", "gateway/v0.1.0")
        self.git("tag", "identity/v9.9.9")
        r = self.run_cli("--component", "gateway", "--bump", "patch")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.1")

    def test_unknown_component_initial_version(self):
        self.commit()
        self.git("tag", "gateway/v0.1.0")
        r = self.run_cli("--component", "nosuch", "--bump", "patch")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.0")

    def test_invalid_bump_rejected(self):
        r = self.run_cli("--component", "gateway", "--bump", "major")
        self.assertEqual(r.returncode, 2)

    def test_outside_git_repo_fails(self):
        r = subprocess.run(
            [sys.executable, str(SCRIPT), "--component", "gateway",
             "--bump", "patch"],
            cwd=self.tmp, env=self.env, capture_output=True, text=True,
        )
        self.assertEqual(r.returncode, 1)


if __name__ == "__main__":
    unittest.main()
