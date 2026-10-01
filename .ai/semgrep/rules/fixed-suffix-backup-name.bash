#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Positive/negative test pair for fixed-suffix-backup-name, derived from
# the PR #17 findings "Use a unique, non-overwriting backup name" and
# "Reserve a backup path that does not already exist".

backup_fixed_names() {
    # ruleid: fixed-suffix-backup-name
    cp "$f" "$f.bak"

    # ruleid: fixed-suffix-backup-name
    mv -f "$conf" "$conf.old"

    # ruleid: fixed-suffix-backup-name
    mv "$db" "$dir/ledger.backup"
}

backup_unique_names() {
    # ok: fixed-suffix-backup-name
    bak=$(mktemp "$dir/hosts.yml.bak.XXXXXX")
    mv -f "$f" "$bak"

    # ok: fixed-suffix-backup-name
    cp "$f" "$dest"

    # ok: fixed-suffix-backup-name
    mv -f "$f" "$f.bak.$RANDOM"

    # ok: fixed-suffix-backup-name
    mv -f "$f" "$f.bak.$(date +%s)"
}
