---
name: session-bootstrap
description: Rebuild working context from authoritative sources after compaction, a new session, or a new task — never trust session memory when the tracker and memory disagree. Use at every session start and after any context reset.
---

# Session Bootstrap

Long sessions rot context: instructions drift, facts go stale, and the
model starts continuing "the previous task" from memory. This protocol
rebuilds working state from authoritative sources every time. Memory is
a cache; the tracker and the repository are the truth.

## Protocol

1. **Load the control plane in order.** Read `.ai/index.yaml` first,
   then its `read_order` list top to bottom, then the `required_rules`
   map — those named skills (upstream-first, backlog,
   small-step-iteration, …) are loaded for every session, not on
   demand. Never crawl `.ai/` — the index is the map; if the index and
   the directory disagree, fix the index (co-change rule) rather than
   guessing.
2. **Orient, don't archive-dive.** `activeContext.md` says what matters
   now; the latest `progress.md` entry says how we got here. Follow
   pointers into decisions and issues for detail — never restate it.
3. **Verify freshness anchors before trusting them.** An issue number
   that no longer exists, an AD that was superseded, a path that moved:
   report the staleness, never silently follow it. Found anchors get
   fixed by the co-change rule, not worked around in-session.
4. **Take the task from the tracker, not from memory.** Re-read the
   issue before continuing anything. If memory contradicts the issue,
   the issue wins.
5. **Spend context deliberately.** Delegate broad exploration to a
   subagent; let `pr_watch.py` poll CI and review state outside the
   model context and report only actionable states. Keep the working
   context for decisions, not for polling.

## Co-change rule

Freshness is maintained by change, not by hope: the PR that changes an
underlying fact updates the corresponding context pointer in the same
PR. A pointer that cannot be kept fresh gets deleted, not annotated.

## Provenance

Adapted from the wait-agent survey (#48), which had adapted the
surrounding control-plane model from this repository — the loop closes.
