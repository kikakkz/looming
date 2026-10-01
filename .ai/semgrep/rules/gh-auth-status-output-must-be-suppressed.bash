#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Positive/negative test pair for gh-auth-status-output-must-be-suppressed,
# derived from the PR #17 finding "suppressed gh auth status output".

status_leaks() {
    # ruleid: gh-auth-status-output-must-be-suppressed
    gh auth status

    # ruleid: gh-auth-status-output-must-be-suppressed
    gh auth status --active

    # ruleid: gh-auth-status-output-must-be-suppressed
    gh auth status --hostname github.com --active 2>&1

    # ruleid: gh-auth-status-output-must-be-suppressed
    gh auth status --hostname github.com --active >/dev/null

    # ruleid: gh-auth-status-output-must-be-suppressed
    gh auth status --hostname github.com --active >"$log"

    # ruleid: gh-auth-status-output-must-be-suppressed
    gh auth status --hostname github.com --active || exit 1

    # ruleid: gh-auth-status-output-must-be-suppressed
    gh auth status --hostname github.com --active && echo ready >/dev/null
}

status_suppressed() {
    # ok: gh-auth-status-output-must-be-suppressed
    gh auth status --hostname github.com --active >/dev/null 2>&1

    # ok: gh-auth-status-output-must-be-suppressed
    gh auth status --hostname github.com --active 2>/dev/null | grep -q token

    # ok: gh-auth-status-output-must-be-suppressed
    gh auth status --hostname github.com --active >"$log" 2>&1
}
