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

- PR bodies prefer `Closes #N` whenever the issue should close at
  merge. Write `Closes` by default; downgrade to `Refs` only when the
  issue must stay open, and say why in the PR body.
- After merging a PR that only `Refs` an issue: immediately either
  close the issue with the closing comment (its work landed) or
  comment why it stays open (genuinely unfinished). Never leave a
  merged-PR reference silent — that residue is how tracker rot
  starts (the #83 miss was exactly this).
- Bots and agents merging PRs follow the same checklist — the merger
  owns the follow-up, not the issue author.

## Closing comment (mandatory)

Before closing, one comment: what was produced or decided, where it
lives (stable identifiers only — AD-N, file paths, spawned issue
numbers), and what follows it. A closed issue must be self-explanatory
years later without reading the whole thread.

## Close reasons

`closed-by-PR` / `closed-as-solidified` / `closed-superseded` (point
at the canonical AD or issue) / `closed-duplicate` / `closed-quiet`
(parked decay, per the backlog skill).

## Holding lists are banned

An issue may not exist merely to hold future work. When an issue
carries patterns, checklists, or decisions other work will need, those
contents distribute into durable docs or real issues, and the issue
closes when the distribution lands.

## Staleness

`status/parked` re-affirmation is the backlog skill's job (monthly
glance, k8s stale semantics). Any non-parked issue untouched for a
release cycle is either advanced or `closed-quiet` — the tracker is
the intake, not the archive.
