#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Positive/negative test pair for the bounded-download rule, derived from
# the PR #17 finding "Bound the release download time".

dl_unbounded() {
    # ruleid: bounded-download
    curl -fL -o "$tmp" "$url"

    # ruleid: bounded-download
    curl -fL --connect-timeout 10 -o "$tmp" "$url"

    # ruleid: bounded-download
    wget -q -O "$tmp" "$url"

    # ruleid: bounded-download
    wget -q --connect-timeout=10 -O "$tmp" "$url"
}

dl_bounded() {
    # ok: bounded-download
    curl -fL --max-time 300 -o "$tmp" "$url"

    # ok: bounded-download
    curl -fL --max-time=300 --connect-timeout 10 -o "$tmp" "$url"

    # ok: bounded-download
    wget -q --timeout=30 -O "$tmp" "$url"

    # ok: bounded-download
    wget -q --timeout=30 --connect-timeout=10 -O "$tmp" "$url"
}
