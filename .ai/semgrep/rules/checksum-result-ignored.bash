#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Positive/negative test pair for checksum-result-ignored, derived from
# the PR #17 finding "Add a checksum-failure installation test": a
# verification whose failure is swallowed verifies nothing.

ignore_result() {
    # ruleid: checksum-result-ignored
    sha256sum -c sums.txt || true

    # ruleid: checksum-result-ignored
    echo "$sum  $arc" | sha256sum -c - || :
}

guard_result() {
    # ok: checksum-result-ignored
    sha256sum -c sums.txt || die "checksum mismatch"

    # ok: checksum-result-ignored
    echo "$sum  $arc" | sha256sum -c - >/dev/null || exit 1

    # ok: checksum-result-ignored
    sha256sum -c sums.txt
    tar -xzf "$arc" -C "$d"
}
