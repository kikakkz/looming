# Component patterns (product plane)

Solidified from #54 (closed); provenance: the wait-agent runtime
survey of 2026-10-01 (evidence list in #48 comments, repository
plane). These four patterns are binding for the product components in
AD-27; per AD-27 §7 each lands with its component's implementation
issue, which inherits the pattern from this document.

## 1. Agent-state signals via reconciled hook configs

Component: **agentruntime**. Versioned, pushed signal envelopes per
supported agent (codex / claude / kimi / …), installed idempotently
into each agent's hook configuration with backup and reconcile
semantics. Scraping transcripts or UI grids is the fallback tier
only, never the primary signal path.

## 2. Single-writer event loop with documented lock order

Components: **every stateful service** — gateway data plane and
control plane, control plane services, CICD orchestration,
agentruntime. One writer per event loop; where loops interact, the
lock acquisition order is written down next to the code, not in a
wiki.

## 3. Monotonic revisions with stale-suppression; sequence-gap detection with replay

Component: **state sync between control plane and nodes**. Revisions
never go backwards; stale revisions are suppressed at the receiver.
Gaps in the sequence are detected and closed by replay from the
append-only record, never by guessing forward.

## 4. Bounded exponential backoff with explicit failure events

Components: **all outbound integrations**. Transient and permanent
failure are distinguished and surfaced as explicit events — no
silent drops. The constraint itself is written into the component's
AGENTS.md so reviewers see it, not just the code.
