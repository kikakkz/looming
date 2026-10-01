#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Positive/negative test pair for checksum-gated-archive-extraction,
# derived from the PR #17 findings "Keep the previous installation until
# extraction succeeds" and "Add a checksum-failure installation test".

extract_without_check() {
    tmp=$(mktemp)
    curl -fL -o "$tmp" "$url"
    # ruleid: checksum-gated-archive-extraction
    tar -xzf "$tmp" -C "$d"
}

extract_long_option_without_check() {
    curl -sSL -o "$arc" "$url"
    echo "unpacking"
    # ruleid: checksum-gated-archive-extraction
    tar --extract -f "$arc" -C "$d"
}

extract_without_check_2() {
    curl -sSL -o "$arc" "$url"
    echo "unpacking"
    # ruleid: checksum-gated-archive-extraction
    tar -xf "$arc"
}

# verification before the download does not gate the later extraction:
# the download replaces the checked archive
verify_before_download() {
    sha256sum -c sums.txt
    curl -sSL -o "$arc" "$url"
    # ruleid: checksum-gated-archive-extraction
    tar -xf "$arc"
}

extract_verified() {
    tmp=$(mktemp)
    # ok: checksum-gated-archive-extraction
    curl -fL -o "$tmp" "$url"
    echo "$sum  $tmp" | sha256sum -c - >/dev/null || die "checksum mismatch"
    tar -xzf "$tmp" -C "$d"
}

extract_manifest_verified() {
    # ok: checksum-gated-archive-extraction
    curl -sSL -o "$arc" "$url"
    sha256sum -c "${arc}.sha256" >/dev/null || die "checksum mismatch"
    tar --extract -f "$arc" -C "$d"
}

extract_not_downloaded() {
    # ok: checksum-gated-archive-extraction
    tar -xzf /opt/dist/app.tar.gz -C "$d"

    # ok: checksum-gated-archive-extraction
    tar -czf "$bundle.tar.gz" "$dir"
}

# a checksum run AFTER the extraction does not lift the gate
extract_first_verify_later() {
    tmp=$(mktemp)
    curl -fL -o "$tmp" "$url"
    # ruleid: checksum-gated-archive-extraction
    tar -xzf "$tmp" -C "$d"
    echo "$sum  $tmp" | sha256sum -c - >/dev/null
}
