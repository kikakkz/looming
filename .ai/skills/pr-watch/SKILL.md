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
3. **Fix on-branch — batched.** Collect every fix for this round into
   ONE commit. Run `make ci-gate` and `bash .ai/tools/check-trailers.sh
   --dco "$(git merge-base origin/main HEAD)..HEAD"`, commit with the
   DCO + `Generated-by:` trailers, push once. The gate's own trailer
   check covers HEAD only and passes no `--dco` — a mid-range unsigned
   commit (fixup, rebase absorb, conflict resolution) passes locally
   and fails PR CI. CodeRabbit bills review events
   per push, not per commit: three commits pushed together cost one
   event; pushed separately, three. Never rename the branch or open a
   new PR. **After pushing, wait for the verdict to land before pushing
   anything else** — an approval submitted seconds before a push is
   auto-dismissed and must be re-acquired, not assumed, and a push
   during a running review supersedes it (paid, discarded).
4. **Hand in once.** `python3 .ai/tools/pr_watch.py nudge <PR>` posts
   exactly one `@coderabbitai full review` per head, only when checks
   are green, no verdict stands, and no threads are open. A clean
   first-pass incremental review is silent by design — the nudge is
   the deterministic verdict lever (~6–13 min).
5. **Repeat** from step 1 until APPROVED or a genuinely new
   changes-requested arrives. Stop on user interrupt.

## Quota discipline

CodeRabbit bills one review event per push (nudges included) against a
rolling allowance; the limits are documented at
https://docs.coderabbit.ai/management/rate-limits. Exhaustion stalls
every PR's gate at once, so the loop spends events deliberately:

- **Open the PR when the branch is ready for feedback**, not at the
  first commit: an open non-draft PR draws a review event on every push
  from the start (`drafts: false` in `.coderabbit.yaml`).
- **Batch pushes** (step 3) and never push between nudge and verdict —
  a push inside the review window supersedes the running review: paid,
  discarded, no feedback.
- **Rate-limited means stop.** The signature is a `@coderabbitai` reply
  of "Action not completed — Review rate limited" (or nudge silence):
  wait out the stated window; the refill is rolling and a blocked
  attempt costs nothing. Check capacity with `@coderabbitai rate limit`
  — chat commands bill separately from reviews.
- **After `auto_pause_after_reviewed_commits` auto rounds** (see
  `.coderabbit.yaml`), automatic incremental reviews pause and the
  guarded nudge becomes the only lever — this skill's step 4.
- **Never GitHub's "Update branch" / "Resolve conflicts" buttons.**
  Each writes a commit (a billed event) and the merge commit carries no
  DCO trailer, failing the trailer gate. Instead rebase the branch onto
  the base locally — the one sanctioned force-push — batched with any
  pending fixes and announced in the PR.

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
- Everything rides the normal branch: no direct pushes, same trailer
  policy as any commit. Force-push only as the batched base-branch
  rebase above — never to evade review.
