# Product domain context map (L0)

Living document (AD-28). Domain view over AD-27's component view —
they are not 1:1 by design. Amendments ride reviewed PRs; the
ubiquitous language lives in [glossary.md](glossary.md); the repeated
method for deriving and refining this map is
[.ai/skills/domain-design/](../../.ai/skills/domain-design/SKILL.md).

## Bounded contexts

| # | Context | Core aggregates | Owns | Explicitly does not own |
|---|---------|-----------------|------|--------------------------|
| 1 | Identity & access | User, Org, ApiKey, Quota, BlueprintIdentity | identity lifecycle, quota policy, ACL subjects | request-time enforcement (stateless gateways enforce, never decide) |
| 2 | Model gateway | Route, ProviderConfig | faithful forwarding, metering **emission**, interception middleware chain | stored meter records; any session state |
| 3 | Session | Session, EventStream, Approval | durable runtime identity, append-only runtime event stream, approval rendering | long-term record storage (→ #9) |
| 4 | Orchestration | Blueprint, Run, Task | planner/executor/worker/judge dispatch, run state machines | sandbox internals (→ #5); policy rules (→ #8) |
| 5 | Sandbox | Sandbox, Pool, Image | model-triggered provisioning, lifecycle, resource limits | credentials (credential proxy, AD-27 §6) |
| 6 | Knowledge | KnowledgeBase, ChunkIndex, RetrievalPipeline | RAG-style domain-knowledge enhancement; source ACLs as item metadata at ingest | agent experience (→ #7); governance of tools (→ #8) |
| 7 | Memory | MemoryItem, ReviewGate | agent experience/decisions at org/project/user scopes; review-gated writes | domain documents (→ #6) |
| 8 | Registry | ToolEntry, McpEntry, SkillEntry, PolicyRule | admission governance for tools/MCP/skills **and policy rules**; enable/disable, A/B admission | execution of policies (PEP is an aspect) |
| 9 | Records/observability | MeterRecord, InteractionBody, DecisionEvent, Trace | every append-only store; retention policies; offline read surface (prompt analysis, evals, compliance export) | runtime semantics (→ #3) |
| 10 | SCM integration | RepoBinding, IssueRef, MergeRequest | issues, review threads, CI status, merge control, webhooks — the anti-corruption layer over GitHub/GitLab CE | pipeline definitions (→ #11) |
| 11 | CI orchestration | Pipeline, Gate, FailCase | org pipeline definitions, gates, judge fail-case 回流 | SCM connectivity (→ #10) |

## Relationships (context map)

- **Session → everyone: Open Host Service + Published Language.** The
  session event protocol is the published language; surfaces and
  harnesses are adapters (AD-27 §3). New surfaces/harnesses conform.
- **SCM integration and Knowledge connectors: Anti-Corruption
  Layer.** GitHub/GitLab and DingTalk/Feishu models never leak inward;
  adapters translate both directions.
- **Identity & access → all contexts: upstream supplier.** Identity
  propagation (delegated user-subject / Agent 365 S2S service-subject,
  AD-27 §6) is the published language; downstream contexts conform.
- **Gateway, orchestration, sandbox → Records: customer–supplier,
  fire-and-forget.** Producers emit; Records owns storage, order of
  arrival, and retention. Producers never read back synchronously.
- **Orchestration → Sandbox: customer–supplier.** Model-triggered
  provisioning requests (the Stripe/Shopify/Spotify/Anthropic
  converged shape from the #66 surveys).
- **Registry → Orchestration/gateway: conformist consumers.** Tool
  and policy admission decisions are consumed as-is; the registry is
  the authority.
- **Memory ↔ Knowledge: separate ways, shared kernel at retrieval.**
  Distinct domains, but both enforce the same retrieval-time ACL
  trimming invariant (AD-27 §2) — the trimming rule is shared kernel,
  the stores are not.

## Cross-cutting aspects (not contexts)

| Aspect | Where it executes | Authority |
|--------|-------------------|-----------|
| PEP (fail-closed per tool dispatch) | orchestration tool dispatch, gateway interception chain, memory/knowledge retrieval | policy rules from Registry (AD-27 §2 invariant) |
| Identity propagation | every cross-context call | Identity & access (AD-27 §6 shapes) |
| Audit event emission | every PEP decision, credential mint, approval | Records/observability owns the event model |
| Metering emission | gateway inline, orchestration runs | Records owns MeterRecord |
| ACL trimming at retrieval | Knowledge, Memory | fail closed on unresolvable ACLs (AD-27 §6) |
| Event backbone transport | all contexts | AD-27 §1: Postgres append-only record + Redis Streams distribution |
| Revisions & idempotency | all stateful contexts | docs/component-patterns.md #3/#4 |

## Levels

- **L0** — this map (product plane).
- **L1** — per-context module design; gateway is first, then D5
  unfreezes.
- **L2** — hexagonal components inside a module, with boundary
  dependency lint in CI from day one.
