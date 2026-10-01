---
name: pr-watch
description: Own the PR review-fix loop end to end for one open PR. Use when a PR is awaiting or reacting to review — watch state, triage findings, fix on-branch, and hand in the review with a single guarded nudge.
---

# PR Watch

Drive one PR from "pushed" to "merged" without human clicks. The
mechanical parts — polling state, triaging findings, pushing fixes,
requesting the verdict — belong to the loop below; the tool
(`.ai/tools/pr_watch.py`) gathers state and guards the writes.

## Loop

1. **Snapshot.** `python3 .ai/tools/pr_watch.py status <PR>`. Classify:
   - `verdict: none` + `checks: green` + `nudges_for_head: 0` → hand in
     (step 4).
   - `verdict: CHANGES_REQUESTED` or `open_threads > 0` → triage (step 2).
   - `verdict: APPROVED` → stop; auto-merge owns the rest.
   - `checks: pending/failing` → wait; never nudge on red.
   - `merged: true` → done.
2. **Triage each open thread** (`findings <PR>`), per the #19 convention:
   - **codified rule** — already covered by `.ai/semgrep/` or another
     check; fix mechanically.
   - **documented rule** — covered by a skill or doc; apply it.
   - **accepted residual** — wrong or inapplicable; record the reason and
     resolve the thread with `resolve <THREAD_ID>`.

   CodeRabbit findings are ~90% real — verify against current code
   before dismissing any.
3. **Fix on-branch.** Run `make ci-gate`, commit with the DCO +
   `Generated-by:` trailers, push to the same branch. Never rename the
   branch or open a new PR. **After pushing, wait for the verdict to
   land before pushing anything else** — an approval submitted seconds
   before a push is auto-dismissed and must be re-acquired, not
   assumed.
4. **Hand in once.** `python3 .ai/tools/pr_watch.py nudge <PR>` posts
   exactly one `@coderabbitai full review` per head, only when checks
   are green, no verdict stands, and no threads are open. A clean
   first-pass incremental review is silent by design — the nudge is
   the deterministic verdict lever (~6–13 min).
5. **Repeat** from step 1 until APPROVED or a genuinely new
   changes-requested arrives. Stop on user interrupt.

## Housekeeping (when merge is blocked despite approval)

- **Unresolved threads**: `findings <PR>` shows them; resolve fixed ones
  with `resolve <THREAD_ID>` (GraphQL — the REST path 404s with our
  token).
- **Stale CHANGES_REQUESTED** on an old head whose findings are all
  fixed: dismiss with `dismiss <REVIEW_ID>`, then nudge.
- **Just-dismissed approval** (the push race): treat as in-flight, nudge
  to re-acquire.

## Hard rules

- One nudge per head. The tool enforces it; do not work around with
  manual comments.
- Never push mid-review: the verdict arrives on the head it was
  requested for.
- Everything rides the normal branch: no direct pushes, no force-push,
  same trailer policy as any commit.
