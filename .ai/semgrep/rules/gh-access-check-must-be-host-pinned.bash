#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Positive/negative test pair for gh-access-check-must-be-host-pinned,
# derived from the PR #17 findings "Pin the repository access check to
# github.com" and "Verify access to this repository, not the GH_REPO
# override".

checks_unpinned() {
    # ruleid: gh-access-check-must-be-host-pinned
    gh auth status --active

    # ruleid: gh-access-check-must-be-host-pinned
    gh pr list --limit 1 >/dev/null 2>&1

    # ruleid: gh-access-check-must-be-host-pinned
    gh api repos/acme/widget --jq .name
}

checks_pinned() {
    # ok: gh-access-check-must-be-host-pinned
    gh auth status --hostname github.com --active >/dev/null 2>&1

    # ok: gh-access-check-must-be-host-pinned
    gh pr list --limit 1 --repo "$slug" >/dev/null 2>&1

    # ok: gh-access-check-must-be-host-pinned
    gh auth status --help 2>/dev/null | grep -q -- --active

    # ok: gh-access-check-must-be-host-pinned
    gh auth token --hostname github.com

    # ok: gh-access-check-must-be-host-pinned
    gh --version
}
