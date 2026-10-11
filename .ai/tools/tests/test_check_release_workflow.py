# SPDX-License-Identifier: Apache-2.0
"""Unit tests for check_release_workflow.py (#159)."""

import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import check_release_workflow as crw

PRECHECK = """\
      - name: Immutable pre-check
        run: |
          set -euo pipefail
          if out=$(docker manifest inspect "$IMAGE" 2>&1); then
            echo "::error::$IMAGE already exists — refusing to repoint"
            exit 1
          fi
          case "$out" in
            *"no such manifest"*|*"manifest unknown"*|*"not found"*|*"NAME_UNKNOWN"*)
              echo "tag absent — proceeding"
              ;;
            *)
              echo "::error::failed closed: $out"
              exit 1
              ;;
          esac
"""

VALID = """\
# SPDX-License-Identifier: Apache-2.0
name: release

on:
  push:
    tags:
      - "gateway/v*.*.*"
      - "identity/v*.*.*"
      - "topology/v*.*.*"
  workflow_dispatch:
    inputs:
      component:
        required: true
        type: string
      version:
        required: true
        type: string
      verify_only:
        required: false
        type: boolean
        default: false

permissions:
  contents: read

jobs:
  resolve:
    outputs:
      git_ref: ${{ steps.parse.outputs.git_ref }}
    steps:
      - name: Resolve component and version
        id: parse
        run: |
          echo "git_ref=refs/tags/$component/$version" >> "$GITHUB_OUTPUT"
  publish:
    needs: resolve
    permissions:
      contents: read
      packages: write
    concurrency:
      group: release-${{ needs.resolve.outputs.component }}-${{ needs.resolve.outputs.version }}
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
        with:
          ref: ${{ needs.resolve.outputs.git_ref }}
          persist-credentials: false
__PRECHECK__      - name: Build and push multi-arch image
        run: docker buildx build --tag ghcr.io/kikakkz/looming/gateway:v0.1.0 --push gateway
""".replace("__PRECHECK__", PRECHECK)


class TagTriggerTests(unittest.TestCase):
    def test_valid_workflow_has_no_problems(self):
        self.assertEqual(crw.problems(VALID), [])

    def test_missing_gateway_tag_pattern(self):
        text = VALID.replace('      - "gateway/v*.*.*"\n', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'gateway/v*.*.*'", problems[0])

    def test_missing_identity_tag_pattern(self):
        text = VALID.replace('      - "identity/v*.*.*"\n', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'identity/v*.*.*'", problems[0])

    def test_missing_topology_tag_pattern(self):
        text = VALID.replace('      - "topology/v*.*.*"\n', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'topology/v*.*.*'", problems[0])

    def test_changed_active_entry_with_stale_comment_fails(self):
        # the active trigger moves to v9, the old pattern survives only
        # in a comment — the check must read active entries, not text
        text = VALID.replace(
            '      - "gateway/v*.*.*"\n',
            '      - "gateway/v9.*.*.*"\n      # "gateway/v*.*.*"\n')
        problems = crw.problems(text)
        self.assertTrue(any("'gateway/v*.*.*'" in p for p in problems))
        self.assertTrue(any("'gateway/v9.*.*.*'" in p for p in problems))

    def test_patterns_only_in_comments_are_not_triggers(self):
        text = VALID.replace('      - "gateway/v*.*.*"\n', "")
        text = text.replace('name: release', '# "gateway/v*.*.*"\nname: release')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'gateway/v*.*.*'", problems[0])


class PermissionsTests(unittest.TestCase):
    def test_missing_packages_write(self):
        text = VALID.replace("      packages: write\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("packages: write", problems[0])

    def test_read_only_packages_permission_fails(self):
        text = VALID.replace("      packages: write", "      packages: read")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("packages: write", problems[0])


class ConcurrencyTests(unittest.TestCase):
    def test_missing_concurrency_group(self):
        text = VALID.replace(
            "      group: release-${{ needs.resolve.outputs.component }}-${{ needs.resolve.outputs.version }}\n",
            "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("no concurrency group", problems[0])

    def test_per_run_key_fails(self):
        text = VALID.replace(
            "group: release-${{ needs.resolve.outputs.component }}-${{ needs.resolve.outputs.version }}",
            "group: release-${{ github.run_id }}")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("resolved", problems[0])

    def test_component_only_key_fails(self):
        text = VALID.replace(
            "group: release-${{ needs.resolve.outputs.component }}-${{ needs.resolve.outputs.version }}",
            "group: release-${{ needs.resolve.outputs.component }}")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("resolved", problems[0])


class PrecheckTests(unittest.TestCase):
    def test_renamed_step_fails(self):
        text = VALID.replace("- name: Immutable pre-check",
                             "- name: Check tag existence")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("immutable pre-check step absent", problems[0])

    def test_bare_inspect_command_fails(self):
        # a naked inspect neither refuses existing tags nor fails closed
        text = VALID.replace(PRECHECK, """\
      - name: Immutable pre-check
        run: docker manifest inspect "$IMAGE"
""")
        problems = crw.problems(text)
        self.assertTrue(any("verdict" in p for p in problems))
        self.assertTrue(any("already exists" in p for p in problems))
        self.assertTrue(any("not-found dispatch" in p for p in problems))

    def test_refusal_without_exit_fails(self):
        text = VALID.replace(
            'echo "::error::$IMAGE already exists — refusing to repoint"\n'
            '            exit 1\n',
            'echo "::error::$IMAGE already exists — refusing to repoint"\n')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("existing-tag branch", problems[0])

    def test_no_not_found_markers_fails(self):
        text = VALID.replace(
            '*"no such manifest"*|*"manifest unknown"*|*"not found"*|*"NAME_UNKNOWN"*)',
            '*"some other verdict"*)')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("not-found verdict", problems[0])

    def test_default_without_fail_closed_exit_fails(self):
        text = VALID.replace(
            'echo "::error::failed closed: $out"\n              exit 1\n',
            'echo "::error::failed closed: $out"\n')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("fail closed", problems[0])

    def test_missing_case_dispatch_fails(self):
        text = VALID.replace('case "$out" in', 'echo "$out"')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("not-found dispatch", problems[0])


class GitRefTests(unittest.TestCase):
    def test_git_ref_without_output_redirect_fails(self):
        text = VALID.replace(
            ' >> "$GITHUB_OUTPUT"',
            '')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("git_ref", problems[0])

    def test_missing_job_output_mapping_fails(self):
        text = VALID.replace(
            "      git_ref: ${{ steps.parse.outputs.git_ref }}\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("outputs do not map", problems[0])

    def test_checkout_without_pinned_ref_fails(self):
        text = VALID.replace(
            "          ref: ${{ needs.resolve.outputs.git_ref }}\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("checkout does not pin", problems[0])

    def test_checkout_pinning_branch_ref_fails(self):
        text = VALID.replace(
            "ref: ${{ needs.resolve.outputs.git_ref }}",
            "ref: ${{ github.ref }}")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("checkout does not pin", problems[0])


class OtherContractTests(unittest.TestCase):
    def test_missing_registry_prefix(self):
        text = VALID.replace("ghcr.io/kikakkz/looming/", "ghcr.io/other/ns/")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("ghcr.io/kikakkz/looming/", problems[0])

    def test_missing_verify_only_input(self):
        text = VALID.replace("      verify_only:\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("verify_only", problems[0])

    def test_empty_workflow_reports_every_piece(self):
        # three tag patterns, each reported on its own line
        self.assertEqual(len(crw.problems("")), 11)


class CliTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.workflow = self.tmp / "release.yml"

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_valid_file_ok(self):
        self.workflow.write_text(VALID, encoding="utf-8")
        self.assertEqual(crw.main(["--workflow", str(self.workflow)]), 0)

    def test_broken_file_fails(self):
        self.workflow.write_text("name: release\n", encoding="utf-8")
        self.assertEqual(crw.main(["--workflow", str(self.workflow)]), 1)

    def test_missing_file_fails(self):
        self.assertEqual(
            crw.main(["--workflow", str(self.tmp / "absent.yml")]), 1)


if __name__ == "__main__":
    unittest.main()
