# SPDX-License-Identifier: Apache-2.0
"""Behavioral coverage for the AD-40 VERSION consistency gate (#172).

The gate step lives inside .github/workflows/release.yml; the text
contract is enforced by check_release_workflow.py, but the parsing rule
itself — the whole file under the canonical exactly-one-version-line
rule, edge whitespace trimmed, internal whitespace rejected — is
behavior. These tests execute the real workflow step against fixture
VERSION files and cross-check every fixture against the canonical
parser (release_version.read_version_file) so shell and Python cannot
drift apart (CodeRabbit #176 round 2).
"""

import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import check_release_workflow as crw
import release_version

ROOT = Path(__file__).resolve().parents[3]
WORKFLOW = ROOT / ".github" / "workflows" / "release.yml"


def run_gate(component: str, version: str, content: str,
             tmp: Path) -> subprocess.CompletedProcess:
    """Execute the real gate step with a fixture VERSION file."""
    comp_dir = tmp / component
    comp_dir.mkdir(parents=True, exist_ok=True)
    (comp_dir / "VERSION").write_text(content, encoding="utf-8")
    script = crw._step_run(WORKFLOW.read_text(encoding="utf-8"),
                           crw.VERSION_GATE_STEP)
    assert script is not None, "gate step missing from release.yml"
    env = os.environ.copy()
    env["COMPONENT"] = component
    env["VERSION"] = version
    return subprocess.run(["bash", "-c", script], cwd=tmp, env=env,
                          capture_output=True, text=True)


class VersionGateBehaviorTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def assert_rejected(self, content: str, version: str = "v0.1.0") -> None:
        r = run_gate("gateway", version, content, self.tmp / "rej")
        self.assertNotEqual(r.returncode, 0,
                            f"gate accepted {content!r} (rc=0)\n{r.stdout}")

    def assert_accepted(self, content: str, version: str = "v0.1.0") -> None:
        r = run_gate("gateway", version, content, self.tmp / "acc")
        self.assertEqual(r.returncode, 0,
                         f"gate rejected {content!r}\n{r.stderr}")
        self.assertIn("consistency gate green", r.stdout)

    def test_single_version_line_accepted(self) -> None:
        self.assert_accepted("# header\n\nv0.1.0\n")

    def test_two_version_lines_rejected(self) -> None:
        # first-line-only reads used to pass this file as v0.1.0
        self.assert_rejected("v0.1.0\nv0.2.0\n")

    def test_internal_whitespace_rejected(self) -> None:
        # blanket `tr -d '[:space:]'` used to pass this as v0.1.0
        self.assert_rejected("v 0.1.0\n")

    def test_version_with_trailing_comment_rejected(self) -> None:
        self.assert_rejected("v0.1.0 # hotfix\n")

    def test_empty_file_rejected(self) -> None:
        self.assert_rejected("# only a comment\n")

    def test_edge_whitespace_trimmed_like_canonical_parser(self) -> None:
        self.assert_accepted("  v0.1.0  \n")

    def test_mismatch_against_tag_rejected(self) -> None:
        self.assert_rejected("v0.1.0\n", version="v0.2.0")

    def test_shell_gate_matches_canonical_parser(self) -> None:
        # Every fixture the shell gate accepts/rejects, the canonical
        # parser must read/raise the same way (AD-40 single source).
        cases = [
            ("# header\n\nv0.1.0\n", "v0.1.0"),
            ("  v0.1.0  \n", "v0.1.0"),
            ("v0.1.0\nv0.2.0\n", None),
            ("v 0.1.0\n", None),
            ("v0.1.0 # hotfix\n", None),
            ("# only a comment\n", None),
        ]
        for content, want in cases:
            with self.subTest(content=content):
                path = self.tmp / "canon" / "gateway" / "VERSION"
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")
                if want is None:
                    with self.assertRaises(ValueError):
                        release_version.read_version_file(path)
                else:
                    self.assertEqual(release_version.read_version_file(path),
                                     want)


if __name__ == "__main__":
    unittest.main()
