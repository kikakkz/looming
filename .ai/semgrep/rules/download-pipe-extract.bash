#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Positive/negative test pair for download-pipe-extract, derived from the
# PR #17 finding "Keep the previous installation until extraction
# succeeds": a piped download has no checksum point.

pipe_extract() {
    # ruleid: download-pipe-extract
    curl -sSL "$url" | tar -xz -C "$d"

    # ruleid: download-pipe-extract
    wget -q -O - "$url" | unzip -q -d "$d" -
}

file_extract_verified() {
    # ok: download-pipe-extract
    curl -sSL -o "$arc" "$url"
    echo "$sum  $arc" | sha256sum -c - >/dev/null
    tar -xzf "$arc" -C "$d"
}
