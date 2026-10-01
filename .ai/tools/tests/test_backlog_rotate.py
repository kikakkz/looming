#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for backlog_rotate.py — API calls mocked at the boundary."""

import datetime
import io
import json
import sys
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import backlog_rotate as br


SKILL_TEXT = """\
# Backlog

**Current lot: #21 (lot 1, opened 2026-09-26).**
"""

LOT_BODY = """\
## Ideas

- [x] Graduated idea -> #47
- [ ] Still vague idea one
- [ ] Still vague idea two

## Carried from lot 1

- [ ] Carried item
"""


class PureLogicTests(unittest.TestCase):
    def test_parse_current_lot(self) -> None:
        self.assertEqual(br.parse_current_lot(SKILL_TEXT), 21)

    def test_parse_current_lot_missing(self) -> None:
        with self.assertRaises(ValueError):
            br.parse_current_lot("# no marker here")

    def test_replace_current_lot(self) -> None:
        out = br.replace_current_lot(SKILL_TEXT, 40)
        self.assertIn("**Current lot: #40", out)
        self.assertNotIn("#21", out)

    def test_parse_items_splits_checked(self) -> None:
        unchecked, graduated = br.parse_items(LOT_BODY)
        self.assertEqual(len(graduated), 1)
        self.assertIn("Graduated idea", graduated[0])
        self.assertEqual(len(unchecked), 3)

    def test_rotation_due_on_item_threshold(self) -> None:
        due, reason = br.rotation_due(10, "2026-10-01T00:00:00Z",
                                      now=datetime.date(2026, 10, 1))
        self.assertTrue(due)
        self.assertIn("10 ungraduated", reason)

    def test_rotation_due_on_idle(self) -> None:
        due, _ = br.rotation_due(2, "2026-07-01T00:00:00Z",
                                 now=datetime.date(2026, 10, 1))
        self.assertTrue(due)

    def test_rotation_not_due_when_active(self) -> None:
        due, _ = br.rotation_due(2, "2026-10-01T00:00:00Z",
                                 now=datetime.date(2026, 10, 1))
        self.assertFalse(due)

    def test_empty_lot_never_rotates_on_idle(self) -> None:
        due, _ = br.rotation_due(0, "2026-01-01T00:00:00Z",
                                 now=datetime.date(2026, 10, 1))
        self.assertFalse(due)

    def test_summary_comment_names_all_buckets(self) -> None:
        text = br.build_summary_comment(21, ["g1"], ["c1"])
        for bucket in ("Graduated", "Carried", "Died"):
            self.assertIn(bucket, text)
        self.assertIn("c1", text)

    def test_successor_body_restates_carried_unchecked(self) -> None:
        body = br.build_successor_body(22, 21, ["idea A"])
        self.assertIn("lot 22", body)
        self.assertIn("lot 21", body)
        self.assertIn("- [ ] idea A", body)


class ApiFake:
    def __init__(self):
        self.created: list[dict] = []
        self.closed: list[int] = []
        self.comments: list[tuple[int, str]] = []
        self.open_cleanup: list[dict] = []

    def call(self, path, payload=None, method=None):
        if payload and payload.get("state") == "closed":
            assert method == "PATCH", f"issue close must be PATCH, got {method}"
            self.closed.append(21)
            return {}
        if path == "/issues/21" and payload is None:
            return {"body": LOT_BODY, "updated_at": "2026-01-01T00:00:00Z"}
        if path == "/issues" and payload and "title" in payload:
            self.created.append(payload)
            return {"number": 22}
        if path.startswith("/issues?state=open"):
            assert "per_page=100" in path
            return self.open_cleanup
        if path.endswith("/comments"):
            self.comments.append((21, payload["body"]))
            return {}
        raise AssertionError(f"unexpected call: {path}")


class RotateFlowTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(__file__).resolve().parent / "_tmp_skill.md"
        self.tmp.write_text(SKILL_TEXT, encoding="utf-8")

    def tearDown(self) -> None:
        self.tmp.unlink(missing_ok=True)

    def test_due_rotation_closes_opens_and_follows_up(self) -> None:
        fake = ApiFake()
        out = io.StringIO()
        with redirect_stdout(out):
            code = br.rotate(fake, self.tmp, dry_run=False)
        self.assertEqual(code, 0)
        self.assertEqual(fake.closed, [21])
        self.assertEqual(len(fake.created), 2)  # successor + follow-up
        self.assertEqual(fake.created[0]["title"],
                         "Backlog: idea parking lot (lot 22)")
        self.assertIn("chore: point backlog skill at lot #22",
                      fake.created[1]["title"])
        # CI checkout is throwaway: the skill file must NOT be rewritten
        self.assertIn("#21", self.tmp.read_text(encoding="utf-8"))

    def test_not_due_is_success_noop(self) -> None:
        body_active = LOT_BODY.replace("2026-01-01", "2026-10-01")
        fake = ApiFake()

        def call_active(path, payload=None, method=None):
            if path == "/issues/21":
                return {"body": body_active,
                        "updated_at": "2026-10-01T00:00:00Z"}
            raise AssertionError(path)

        with mock.patch.object(fake, "call", call_active):
            with redirect_stdout(io.StringIO()):
                code = br.rotate(fake, self.tmp, dry_run=False)
        self.assertEqual(code, 0)
        self.assertEqual(fake.created, [])

    def test_followup_issue_is_idempotent(self) -> None:
        fake = ApiFake()
        fake.open_cleanup = [{"title": "chore: point backlog skill at lot #22"}]
        with redirect_stdout(io.StringIO()):
            br.rotate(fake, self.tmp, dry_run=False)
        self.assertEqual(len(fake.created), 1)  # successor only

    def test_dry_run_writes_nothing(self) -> None:
        fake = ApiFake()
        with redirect_stdout(io.StringIO()):
            code = br.rotate(fake, self.tmp, dry_run=True)
        self.assertEqual(code, 0)
        self.assertEqual(fake.created, [])
        self.assertEqual(fake.closed, [])


if __name__ == "__main__":
    unittest.main()
