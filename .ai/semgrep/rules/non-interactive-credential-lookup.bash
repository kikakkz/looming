#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Positive/negative test pair for non-interactive-credential-lookup,
# derived from the PR #17 findings "Keep credential lookup non-interactive"
# and "Disable askpass for the credential lookup".

lookup_prompts() {
    # ruleid: non-interactive-credential-lookup
    cred=$(git credential fill <<<"$request")

    # ruleid: non-interactive-credential-lookup
    printf '%s\n' "$request" | git credential fill

    # ruleid: non-interactive-credential-lookup
    git clone https://github.com/acme/widget.git "$srcdir"

    # ruleid: non-interactive-credential-lookup
    git push origin main

    # ruleid: non-interactive-credential-lookup
    GIT_TERMINAL_PROMPT=0 git credential fill <<<"$request"
}

lookup_non_interactive() {
    # ok: non-interactive-credential-lookup
    cred=$(GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git credential fill <<<"$request")

    # ok: non-interactive-credential-lookup
    cred=$(env -u GIT_DIR GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git -c core.askPass= credential fill 2>/dev/null <<<"$request")

    # ok: non-interactive-credential-lookup
    GIT_ASKPASS="" GIT_TERMINAL_PROMPT=0 git clone https://github.com/acme/widget.git "$srcdir"

    # ok: non-interactive-credential-lookup
    env -u GIT_SSH_COMMAND GIT_ASKPASS= GIT_TERMINAL_PROMPT=0 git ls-remote origin
}
