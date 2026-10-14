#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for release_version.py (#166, AD-40 file source #172)."""

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
        for bump in ("patch", "minor", "major"):
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

    def test_major_bump_zeroes_minor_and_patch(self):
        self.assertEqual(
            release_version.next_version(["gateway/v0.9.9"], "major"), "v1.0.0")
        self.assertEqual(
            release_version.next_version(["gateway/v1.2.3"], "major"), "v2.0.0")

    def test_major_from_max_semver(self):
        tags = ["gateway/v0.1.9", "gateway/v0.1.10", "gateway/v0.1.2"]
        self.assertEqual(release_version.next_version(tags, "major"), "v1.0.0")

    def test_bumps_continue_after_a_major(self):
        # A major release is an ordinary tag: patch and minor derive
        # from it exactly like from any other version.
        tags = ["gateway/v2.0.0"]
        self.assertEqual(release_version.next_version(tags, "patch"), "v2.0.1")
        self.assertEqual(release_version.next_version(tags, "minor"), "v2.1.0")
        self.assertEqual(
            release_version.next_version(tags, "major"), "v3.0.0")

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
            release_version.next_version(["gateway/v0.1.0"], "micro")

    def test_generator_input_accepted(self):
        self.assertEqual(
            release_version.next_version(iter(["gateway/v2.3.4"]), "minor"),
            "v2.4.0",
        )


class BumpVersionTests(unittest.TestCase):
    """bump_version: next version from an explicit current version (#172)."""

    def test_patch(self):
        self.assertEqual(release_version.bump_version("v0.1.0", "patch"), "v0.1.1")

    def test_minor_resets_patch(self):
        self.assertEqual(release_version.bump_version("v0.1.7", "minor"), "v0.2.0")

    def test_major_zeroes_lower_fields(self):
        self.assertEqual(release_version.bump_version("v0.9.9", "major"), "v1.0.0")

    def test_unreleased_current_is_initial_version(self):
        # None/empty current (no VERSION file, no tags) starts at v0.1.0
        # for every bump.
        for bump in ("patch", "minor", "major"):
            with self.subTest(bump=bump):
                self.assertEqual(release_version.bump_version(None, bump), "v0.1.0")
                self.assertEqual(release_version.bump_version("", bump), "v0.1.0")

    def test_whitespace_is_stripped(self):
        self.assertEqual(release_version.bump_version("  v1.2.3\n", "patch"),
                         "v1.2.4")

    def test_bad_current_raises(self):
        with self.assertRaises(ValueError):
            release_version.bump_version("junk", "patch")
        with self.assertRaises(ValueError):
            release_version.bump_version("v1.0", "minor")
        with self.assertRaises(ValueError):
            release_version.bump_version("v1.0.0-beta", "major")

    def test_unknown_bump_raises(self):
        with self.assertRaises(ValueError):
            release_version.bump_version("v0.1.0", "micro")


class ReadVersionFileTests(unittest.TestCase):
    """read_version_file: the AD-40 canonical source parser (#172)."""

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.f = self.tmp / "VERSION"

    def test_single_line(self):
        self.f.write_text("v0.1.0\n", encoding="utf-8")
        self.assertEqual(release_version.read_version_file(self.f), "v0.1.0")

    def test_header_comments_and_blanks_skipped(self):
        # The repo's real files carry an SPDX line and an explanatory
        # header — exactly one non-comment line must remain.
        self.f.write_text(
            "# SPDX-License-Identifier: Apache-2.0\n"
            "\n"
            "# Canonical component version (AD-40): ...\n"
            "# ... more header ...\n"
            "v0.3.1\n",
            encoding="utf-8")
        self.assertEqual(release_version.read_version_file(self.f), "v0.3.1")

    def test_padded_line_is_trimmed(self):
        self.f.write_text("  v0.1.0  \n", encoding="utf-8")
        self.assertEqual(release_version.read_version_file(self.f), "v0.1.0")

    def test_missing_file_returns_none(self):
        self.assertIsNone(release_version.read_version_file(self.f))

    def test_empty_file_raises(self):
        self.f.write_text("# only a comment\n", encoding="utf-8")
        with self.assertRaises(ValueError):
            release_version.read_version_file(self.f)

    def test_junk_raises(self):
        self.f.write_text("banana\n", encoding="utf-8")
        with self.assertRaises(ValueError):
            release_version.read_version_file(self.f)

    def test_two_version_lines_raise(self):
        # Not single-line: the file must name exactly one version.
        self.f.write_text("v0.1.0\nv0.2.0\n", encoding="utf-8")
        with self.assertRaises(ValueError):
            release_version.read_version_file(self.f)

    def test_prerelease_rejected(self):
        self.f.write_text("v0.1.0-beta\n", encoding="utf-8")
        with self.assertRaises(ValueError):
            release_version.read_version_file(self.f)

    def test_two_fields_rejected(self):
        self.f.write_text("v0.1\n", encoding="utf-8")
        with self.assertRaises(ValueError):
            release_version.read_version_file(self.f)


class _RepoFixture(unittest.TestCase):
    """A throwaway git repo to run the CLI against."""

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

    def run_cli(self, *args: str, cwd: Path | None = None,
                stdin: str | None = None) -> subprocess.CompletedProcess:
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            cwd=cwd or self.repo, env=self.env, capture_output=True, text=True,
            input=stdin,
        )

    def commit(self) -> None:
        (self.repo / "f.txt").write_text("x", encoding="utf-8")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "init")


class TagCliTests(_RepoFixture):
    """The --from-tags path: derive from the cwd repo's git tags (#166)."""

    def test_patch_from_tag(self):
        self.commit()
        self.git("tag", "gateway/v0.1.0")
        r = self.run_cli("--component", "gateway", "--bump", "patch",
                         "--from-tags")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.1")

    def test_minor_from_tag(self):
        self.commit()
        self.git("tag", "gateway/v0.9.9")
        r = self.run_cli("--component", "gateway", "--bump", "minor",
                         "--from-tags")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.10.0")

    def test_major_from_tag(self):
        self.commit()
        self.git("tag", "gateway/v1.2.3")
        r = self.run_cli("--component", "gateway", "--bump", "major",
                         "--from-tags")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v2.0.0")

    def test_no_tags_initial_version(self):
        self.commit()
        r = self.run_cli("--component", "topology", "--bump", "patch",
                         "--from-tags")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.0")

    def test_component_filter_isolates_versions(self):
        self.commit()
        self.git("tag", "gateway/v0.1.0")
        self.git("tag", "identity/v9.9.9")
        r = self.run_cli("--component", "gateway", "--bump", "patch",
                         "--from-tags")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.1")

    def test_unknown_component_initial_version(self):
        self.commit()
        self.git("tag", "gateway/v0.1.0")
        r = self.run_cli("--component", "nosuch", "--bump", "patch",
                         "--from-tags")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.0")

    def test_current_from_tags(self):
        self.commit()
        self.git("tag", "gateway/v0.1.0")
        self.git("tag", "gateway/v0.2.0")
        r = self.run_cli("--component", "gateway", "--from-tags")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.2.0")

    def test_current_from_tags_absent(self):
        self.commit()
        r = self.run_cli("--component", "gateway", "--from-tags")
        self.assertEqual(r.returncode, 3)
        self.assertEqual(r.stdout, "")

    def test_invalid_bump_rejected(self):
        r = self.run_cli("--component", "gateway", "--bump", "micro")
        self.assertEqual(r.returncode, 2)

    def test_outside_git_repo_fails(self):
        r = self.run_cli("--component", "gateway", "--bump", "patch",
                         "--from-tags", cwd=self.tmp)
        self.assertEqual(r.returncode, 1)


class FileCliTests(_RepoFixture):
    """The AD-40 default path: derive from the component's VERSION file."""

    def write_version(self, component: str, content: str) -> Path:
        d = self.repo / component
        d.mkdir(exist_ok=True)
        f = d / "VERSION"
        f.write_text(content, encoding="utf-8")
        return f

    def test_patch_from_default_file_lookup(self):
        self.commit()
        self.write_version("gateway", "v0.1.0\n")
        r = self.run_cli("--component", "gateway", "--bump", "patch")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.1")

    def test_explicit_file(self):
        self.commit()
        f = self.write_version("elsewhere", "v0.9.9\n")
        r = self.run_cli("--component", "gateway", "--bump", "minor",
                         "--file", str(f))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.10.0")

    def test_current_from_file(self):
        self.commit()
        self.write_version("gateway", "v0.3.1\n")
        r = self.run_cli("--component", "gateway")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.3.1")

    def test_current_from_file_absent(self):
        self.commit()
        r = self.run_cli("--component", "gateway")
        self.assertEqual(r.returncode, 3)
        self.assertEqual(r.stdout, "")

    def test_missing_file_bumps_to_initial(self):
        # Unreleased component: any bump lands on the initial v0.1.0.
        self.commit()
        for bump in ("patch", "minor", "major"):
            with self.subTest(bump=bump):
                r = self.run_cli("--component", "topology", "--bump", bump)
                self.assertEqual(r.returncode, 0, r.stderr)
                self.assertEqual(r.stdout.strip(), "v0.1.0")

    def test_tags_are_ignored_by_default(self):
        # The re-sourced derivation never consults tags: a higher tag
        # must not move the math past the file's version (#172).
        self.commit()
        self.git("tag", "gateway/v9.9.9")
        self.write_version("gateway", "v0.1.0\n")
        r = self.run_cli("--component", "gateway", "--bump", "patch")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.1.1")

    def test_bad_file_content_fails(self):
        self.commit()
        self.write_version("gateway", "banana\n")
        r = self.run_cli("--component", "gateway", "--bump", "patch")
        self.assertEqual(r.returncode, 1)
        self.assertIn("VERSION", r.stderr)

    def test_header_comments_accepted(self):
        self.commit()
        self.write_version("gateway", "# header\n\nv0.2.0\n")
        r = self.run_cli("--component", "gateway", "--bump", "patch")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.2.1")

    def test_stdin_via_dash(self):
        # release.sh reads the VERSION content at a fetched ref through
        # this path (git show FETCH_HEAD:<component>/VERSION | tool).
        self.commit()
        r = self.run_cli("--component", "gateway", "--bump", "minor",
                         "--file", "-", stdin="v0.2.0\n")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v0.3.0")

    def test_stdin_current_absent(self):
        self.commit()
        r = self.run_cli("--component", "gateway", "--file", "-", stdin="")
        self.assertEqual(r.returncode, 3)

    def test_no_git_repo_needed_for_file_mode(self):
        # File derivation is repo-independent: it must work outside any
        # git checkout (a tagless clone is not even required).
        d = self.tmp / "plain"
        d.mkdir()
        (d / "VERSION").write_text("v1.2.3\n", encoding="utf-8")
        r = self.run_cli("--component", "gateway", "--bump", "patch",
                         "--file", str(d / "VERSION"), cwd=d)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "v1.2.4")


if __name__ == "__main__":
    unittest.main()
