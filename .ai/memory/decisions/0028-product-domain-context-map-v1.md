---
number: 28
title: "product domain context map v1"
date: "2026-10-02"
updated: "2026-10-02"
status: "accepted"
supersedes: []
adopted-at: "2026-10-02"
---

# AD-28 — product domain context map v1

## Context

AD-27 froze the product's component/deployment view, but the domain
view was never drawn — every component discussion ("what does the
gateway own?") had no bounded-context map to answer it. The discussion
that produced the map also surfaced three modeling errors in the
first draft: knowledge/memory/registry were lumped as one "knowledge
plane" despite being three domains; "policy/audit" was proposed as a
context when it is actually three different things (governed rules,
append-only records, an enforcement mechanism); and long-term record
storage was assigned to the session context, which contradicts the
mature pattern that stateless producers never own storage (Langfuse /
OTel collector / Shopify Aquifer / Coinbase trace store — all from
the #66 surveys or public equivalents).

## Decision

The product plane is eleven bounded contexts (full map and
relationships in `docs/architecture/context-map.md`, ubiquitous
language in `docs/architecture/glossary.md`):

1. **Identity & access** — User, Org, ApiKey, Quota (quota aggregate
   lives here; the gateway only enforces it at request time and stays
   stateless), BlueprintIdentity (Agent 365 S2S shapes).
2. **Model gateway** — faithful forwarding, metering emission,
   risk-interception middleware chain, provider slots. Stateless; a
   producer of records, never an owner.
3. **Session** — durable identity, append-only runtime event stream,
   approval rendering. Proprietary core (AD-27 §3); owns runtime
   state only, no record storage.
4. **Orchestration** — blueprints, planner/executor/worker/judge
   dispatch, run state machines.
5. **Sandbox** — model-triggered provisioning, lifecycle, images,
   pools.
6. **Knowledge** — RAG-style domain-knowledge enhancement (bases,
   chunk indexes, retrieval pipelines); source ACLs arrive with items
   at ingest; retrieval-time trimming is the enforced boundary.
7. **Memory** — agent experience and decisions at org/project/user
   scopes; writes pass a review gate.
8. **Registry** — admission governance for tools, MCP servers,
   skills, **and policy rules** (rules are review-gated artifacts
   with the same lifecycle).
9. **Records/observability** — owns every append-only store:
   MeterRecord, InteractionBody, DecisionEvent, Trace. Retention
   policies live here; offline consumers (prompt analysis, evals,
   compliance export) read here.
10. **SCM integration** — ScmProvider: issues, review threads, CI
    status, merge control, webhooks; an anti-corruption layer over
    GitHub/GitLab CE. Independent of CI orchestration (AD-26).
11. **CI orchestration** — org pipelines, gates, judge fail-case
   回流.

The proposed "policy/audit context" is dissolved: policy rules belong
to Registry, audit decision events belong to Records/observability,
and the PEP is a cross-cutting enforcement aspect evaluated before
every tool dispatch (AD-27's fail-closed invariant unchanged — this
is a domain-view re-homing, not a semantic change).

The map is a living document: omissions and new contexts amend
`docs/architecture/context-map.md` through reviewed PRs.

## Consequences

- Every future feature repeats the same top-down method, codified in
  `.ai/skills/domain-design/`: L0 product map (this AD) → L1 per-
  context module design → L2 hexagonal components; gateway is the
  first L1, after which D5 unfreezes.
- Context boundaries become CI-enforced dependency rules from the
  first Go component (ArchUnit-style lint in the gateway skeleton).
- AD-27 stands: this is the domain view over the same architecture;
  no component decision is reopened.
- The four component patterns in `docs/component-patterns.md` map
  onto contexts (e.g. single-writer event loop applies to every
  stateful context; distribution tracked by #82).
