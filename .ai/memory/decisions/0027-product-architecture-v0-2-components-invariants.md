---
number: 27
title: "Product architecture v0.2: components, invariants, surfaces"
date: "2026-10-02"
updated: "2026-10-02"
status: "accepted"
supersedes: [23]
adopted-at: "2026-10-02"
---

# AD-27 — Product architecture v0.2: components, invariants, surfaces

## Context

AD-23 mapped five bounded contexts from the original five-function
definition. Since then, three delegated research surveys grounded the
design in production evidence: the four-company agent-infrastructure
survey (Stripe Minions, Shopify Aquifer/River, Spotify Honk/Portal,
Coinbase Forge/Mux, plus Anthropic's managed-agents writeup — first-party
sources), the enterprise permission survey (Microsoft OBO/Agent 365,
Lark/DingTalk token models, Graph connectors ACL model, Vercel/AuthZed/AWS
policy patterns), and the agent-native constitution survey. The
architecture discussion in #66 produced decisions D1–D15, reviewed and
amended by the maintainer through several rounds (SCM independence,
sandbox-plane independence, gateway semantics, third-party CLI
degradation-to-surface, routing by task class). This record consolidates
the converged state and supersedes AD-23's context map.

## Decision

**1. Component map v0.2 — seven components plus two cross-cutting
planes.** Gateway data plane and control plane; **ScmProvider as an
independent component** (SCM without CICD is a valid deployment);
**sandbox plane as an independent component** (model-triggered,
elastically provisioned, multi-host Docker fleet from day one — hosts
register via the bundle topology wizard, warm pool is a policy
parameter, K8s is the graduation path behind the same provisioning
API); agentruntime; memory & tools registry; CICD orchestration
(independent of ScmProvider). Cross-cutting: the **session/event
backbone** (Postgres append-only record + Redis Streams distribution;
three stores, three consistencies — inline metering at the gateway,
harness-written session log, async raw bodies; OTel from day one) and
the **policy/audit plane** (PEP before every tool dispatch, credential
proxy, dual-identity audit events).

**2. The two-layer principle: invariants never pluggable, implementations
always pluggable.** Invariants are axioms with no off switch: gateway
authn/quota/faithful-forwarding/metering; ACL-trimmed retrieval (no
unauthorized content enters a model context); fail-closed PEP per tool
dispatch; credentials never enter sandboxes or model context; every
delegated call carries the invoking user's identity with the agent as
actor (service-mode identity per §6); mandatory append-only event
logging. Everything else is a slot behind a
small contract that carries its invariants along: model provider,
runtime harness, memory backend (gptmem, mem0/mem3, mem-palace…),
sandbox backend, SCM adapter, knowledge connectors, policy engine
(OPA ▸ Cedar), vault backend, individual tools/MCP/skills. A slot opens
only when the ecosystem shows multiple credible implementations and the
semantics fit a small contract.

**3. Surface / Session / Harness layering.** Surfaces (our CLI, IM
bots, later web) are thin renderers of the session stream. The session
layer — durable identity plus append-only event stream — is proprietary
core and the single source of truth for rendering, audit, replay, and
approvals. Harnesses (our runtime, or codex/claude/kimi-code/goose
inside sandboxes) attach through a harness-adapter protocol. Adding a
surface is adding a renderer; adding a harness is adding an adapter;
neither touches the session layer.

**4. Third-party agent CLIs degrade to surfaces via MCP.** Looming
ships an MCP server (`ker_start` / `ker_events` / `ker_reply` /
`ker_stop`) and a per-CLI injectable **profile** (data, not fork —
idempotent config reconciliation, reversible). With the profile
injected, the CLI's memory and registry tools are the platform's
(approved tools only, hidden pre-model), and the invariant holds
mechanically: **spawning a sub-agent routes to ker** — the local
spawn tool is hidden, so `ker_start` is the only door. The profile's
soft habit: self-contained tasks prefer the sub-agent pattern (the
line where orchestration pays for itself); trivial inline edits stay
local but still use platform memory/registry tools, metered, with a
memory summary written. Escapes are explicit: `@local` (pure BYO,
deliberately out of platform audit) and `worker=claude|codex`
(backend orchestration with that CLI executing inside the worker
sandbox).

**5. Gateway semantics.** The gateway forwards faithfully; streaming
and retry semantics belong to the client. Risk interception is a
middleware-chain plugin (Traefik analogy — jev/laya are one chain-link
implementation class). Prompt analysis is offline batch, reading the
interaction store directly, writing to memory/registry.

**6. Permissions (enterprise gate).** The credential proxy owns AD-6's
per-run, repository-scoped credentials: it mints them and binds them at
clone/setup time (git tokens wired into the remote); they never appear
in the harness, the sandbox filesystem, or model context — AD-27's
isolation invariant and AD-6's scoping compose, not compete. Two token
modes only: delegated
(user token exchanged downstream; user = subject, agent = actor) and
service (sync/background, never mixed with user-triggered calls;
audited in the Agent 365 S2S shape — the agent identity is the
subject, its owning blueprint is the owner).
Knowledge connectors carry source ACLs as item metadata at ingest;
retrieval-time trimming is the enforced boundary; fail closed on
unresolvable ACLs; per-source identity mapping; event-audited.

**7. Deferred with named owners.** D8 (REQUIRES_APPROVAL human loop:
rendering, timeouts, escalation) gets its own design issue before
components freeze on the PEP shape. IM entry is phase 2 (CLI first).
`.ai/` split triggers were rejected — single repo for the foreseeable
future. E2E runner budget stays open. #54's four patterns distribute
into the component implementation issues created under this record.

## Consequences

- AD-23 is superseded; the README and #66 diagrams narrate the same
  v0.2 map for readers.
- The next action is D5: the gateway's first slice (go.mod, hexagonal
  skeleton per AD-23's layering, Go CI job) under small-step-iteration.
- Component implementation issues inherit their patterns from #54;
  when those exist, #54 closes.
- Every future surface or harness is a renderer or an adapter against
  the session protocol — the proprietary core does not grow by
  integration count.
