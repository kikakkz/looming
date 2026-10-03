# SPDX-License-Identifier: Apache-2.0
"""Unit tests for check_pr_body.py (#92)."""

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from check_pr_body import main, violations


class ViolationsTests(unittest.TestCase):
    def test_closes_needs_no_reason(self):
        self.assertEqual(violations("Closes #54\n\nBody."), [])

    def test_fixes_needs_no_reason(self):
        self.assertEqual(violations("Fixes #12 — regression in gate"), [])

    def test_refs_with_dash_reason_ok(self):
        self.assertEqual(
            violations("Refs #79 — stays open until the PEP shape lands"), [])

    def test_refs_with_colon_reason_ok(self):
        self.assertEqual(
            violations("Refs: #82 pending first component issue"), [])

    def test_refs_with_paren_reason_ok(self):
        self.assertEqual(
            violations("Refs #21 (superseded by AD-31, kept for context)"), [])

    def test_refs_case_insensitive(self):
        self.assertEqual(violations("references #90 — see the checklist"), [])

    def test_bare_refs_fails(self):
        self.assertEqual(len(violations("Refs #54")), 1)

    def test_refs_at_line_end_fails(self):
        self.assertEqual(len(violations("Some text\n\nRefs #79")), 1)

    def test_short_reason_fails(self):
        self.assertEqual(len(violations("Refs #79 — later")), 1)

    def test_mixed_closes_and_refs(self):
        body = "Closes #92\n\nRefs #79 — stays open until the PEP shape lands"
        self.assertEqual(violations(body), [])

    def test_mixed_closes_and_bare_refs_fails(self):
        body = "Closes #92\n\nRefs #79"
        self.assertEqual(len(violations(body)), 1)

    def test_bare_hash_without_keyword_ignored(self):
        self.assertEqual(violations("See also #88 for the diagrams"), [])

    def test_fenced_block_ignored(self):
        body = "Text\n\n```\nRefs #54\n```\n\nCloses #92"
        self.assertEqual(violations(body), [])

    def test_html_comment_ignored(self):
        body = "Closes #92\n\n<!-- Refs #54 -->"
        self.assertEqual(violations(body), [])

    def test_no_references_ok(self):
        self.assertEqual(violations("Just prose, no references."), [])

    def test_multiple_bare_refs_reports_each(self):
        body = "Refs #79\nRefs #82 — waiting on first component issue"
        self.assertEqual(len(violations(body)), 1)


class CliTests(unittest.TestCase):
    def test_empty_body_skips(self):
        self.assertEqual(main(["--body", ""]), 0)

    def test_clean_body_ok(self):
        self.assertEqual(main(["--body", "Closes #92"]), 0)

    def test_violating_body_fails(self):
        self.assertEqual(main(["--body", "Refs #54"]), 1)


if __name__ == "__main__":
    unittest.main()

class HardeningTests(unittest.TestCase):
    def test_colon_form_bare_fails(self):
        self.assertEqual(len(violations("Refs: #82")), 1)

    def test_colon_form_with_reason_ok(self):
        self.assertEqual(
            violations("Refs: #82 pending first component issue"), [])

    def test_next_reference_bounds_reason(self):
        body = "Refs #1 Refs #2 — deferred until phase two"
        problems = violations(body)
        self.assertEqual(len(problems), 1)
        self.assertIn("`Refs #1`", problems[0])

    def test_tilde_fence_ignored(self):
        body = "~~~\nRefs #54\n~~~\nCloses #92"
        self.assertEqual(violations(body), [])

    def test_fence_closes_only_with_same_char_longer_run(self):
        body = "```note\nRefs #79\n`` prose continues\nCloses #92"
        self.assertEqual(violations(body), [])

    def test_short_closer_does_not_close_fence(self):
        body = "````\nRefs #79\n```\nstill fenced\n````\nCloses #92"
        self.assertEqual(violations(body), [])

    def test_unclosed_fence_swallows_rest(self):
        body = "```\nRefs #54"
        self.assertEqual(violations(body), [])

    def test_inline_backticks_do_not_open_fence(self):
        body = "use `code` here\nRefs #79 — stays open until phase two"
        self.assertEqual(violations(body), [])

    def test_multiline_comment_does_not_merge_reason(self):
        body = "Refs #79\n<!-- a\nb -->\nCloses #92"
        self.assertEqual(len(violations(body)), 1)

    def test_same_line_comment_removed(self):
        body = "Refs #79 <!-- note -->\nCloses #92"
        self.assertEqual(len(violations(body)), 1)
