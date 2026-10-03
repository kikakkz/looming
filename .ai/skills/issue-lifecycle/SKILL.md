---
name: issue-lifecycle
description: Every issue is a disposable tracker with a mandatory close — solidify durable output to ADs/docs/skills, link it in the closing comment, then close. No holding-list issues, no perpetual open issues.
---

# Issue lifecycle

An issue is a disposable tracker, never a knowledge store (k8s
lifecycle semantics, vscode grooming — methodology is upstream-first).
Durable knowledge that must outlive an issue is solidified first,
linked, then the issue closes. A perpetually open issue is a process
smell, not a treasure chest (#54 and #66 were the counter-examples;
this skill exists because of that review finding).

## Close conditions, by kind

- **Implementation** — the PR merges; `Closes #N` in the PR body.
- **kind/design** — the decision is recorded (AD-N via `adr_manager`,
  or a `docs/` artifact) and the closing comment links the record.
- **Survey / spike / research** — findings are distilled into a durable
  doc (`docs/` or `.ai/memory/`) and linked. Raw evidence may stay in
  the thread; the summary may not.
- **bug** — closed by the fixing PR, or closed-not-planned with the
  reason stated in the thread.

## Merge-time checklist (the closer owes this)

The merge is **not complete** until every issue referenced in the PR
body is processed — closure is part of the merge action, never a
later cleanup (AD-30: reliability is by construction; there is no
correction layer).

- **At PR creation, immediately opt the PR into auto-merge
  (squash).** Auto-merge is per-PR: the repo setting only permits it,
  and an unopted PR waits forever no matter how green it gets. With
  it on, a full-review approval + green CI + resolved conversations
  merge without any polling; CHANGES_REQUESTED suspends, and a fresh
  approval resumes. After each findings round: disposition every open
  thread first (fix it, or record an accepted residual per the triage
  rule), then `@coderabbitai resolve` clears them so the fresh
  approval triggers the merge. The command resolves **all** CodeRabbit
  threads — resolving a still-valid finding to unblock auto-merge is
  out of process (PR #96 review finding).
- PR bodies prefer `Closes #N` whenever the issue should close at
  merge. Write `Closes` by default; downgrade to `Refs` only when the
  issue must stay open, and say why in the PR body.
- After merging a PR that only `Refs` an issue: immediately either
  close the issue with the closing comment (its work landed) or
  comment why it stays open (genuinely unfinished). Never leave a
  merged-PR reference silent — that residue is how tracker rot
  starts (the #83 miss was exactly this).
- Bots and agents merging PRs follow the same checklist — the merger
  owns the follow-up, not the issue author. An automated merge loop
  that skips this step is an incomplete merge.

## Closing comment (mandatory)

Before closing, one comment: what was produced or decided, where it
lives (stable identifiers only — AD-N, file paths, spawned issue
numbers), and what follows it. A closed issue must be self-explanatory
years later without reading the whole thread.

## Close reasons

`closed-by-PR` / `closed-as-solidified` / `closed-deferred` (trigger
stated; re-entry at intake time, per the backlog skill and AD-31) /
`closed-not-planned` (dropped — not worth a durable record, per AD-31)
/ `closed-superseded` (point at the canonical AD or issue) /
`closed-duplicate` / `closed-quiet` (inactive; reopening is cheap).

## Holding lists are banned

An issue may not exist merely to hold future work. When an issue
carries patterns, checklists, or decisions other work will need, those
contents distribute into durable docs or real issues, and the issue
closes when the distribution lands.

## Staleness

An open issue untouched for a release cycle is either advanced or
`closed-quiet` — the tracker is the intake, not the archive. There is
no parked state to re-affirm (AD-31): deferred work is already closed
with its trigger, and its re-entry rides intake, not a schedule.
