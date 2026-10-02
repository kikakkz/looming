---
number: 29
title: "gateway thin slot host"
date: "2026-10-02"
updated: "2026-10-02"
status: "accepted"
supersedes: []
adopted-at: "2026-10-02"
---

# AD-29 — gateway thin slot host

## Context

The gateway L1 design needs a build-vs-adopt call first. Spike #85
surveyed the whole field: LiteLLM, Kong, Apache APISIX, Envoy AI
Gateway / Agent Router, Bifrost, Higress. Capability checklists are
strong everywhere (virtual keys, budgets, guardrail hooks, provider
routing), but every product is a **model-management platform**: their
value is provider normalization, their metering is their own schema,
their plugin models are in-process callbacks, and each carries its
own operational store. Four invariant mismatches are isomorphic
across the field:

1. Faithful forwarding (AD-27 §5: payload-preserving, client-owned
   streaming/retry) vs normalization as the product's core value.
2. Our event shapes (MeterRecord / InteractionBody, three stores and
   three consistencies) vs engine-private spend/log schemas — a
   second source of truth.
3. A first-class interception middleware chain (Traefik analogy,
   jev/laya as local chain links) vs engine-internal hooks.
4. Bundle lifecycle ownership (enable/disable, first-boot topology)
   over a foreign monolith.

Plus supply-chain and licensing burdens for a shipped bundle
(LiteLLM's 2026-03 PyPI compromise; unresolved MIT/Enterprise
boundary).

The maintainer's product philosophy is decisive: Looming provides
**slots plus best-practice packaging over mature OSS**; build only
what has no mature solution or carries risk. Users get a default
component that works out of the box, and may swap in a compatible OSS
alternative.

## Decision

The gateway is a **thin slot host**, not a LiteLLM-class rebuild.

**The Looming front layer is the terminating proxy.** All traffic
terminates on our layer; the forwarding engine (default: our thin
implementation; alternatives: LiteLLM/Kong/Envoy mounted behind us in
passthrough mode) sits in a backend slot. Because traffic passes
through our layer, authentication fan-in, the interception chain, and
event generation are in-band and engine-independent:

- MeterRecord is generated inline as the response stream (SSE chunks
  counted as they pass); InteractionBody is emitted async. Append-
  only records are generated **at request time by our layer** — never
  ETL'd from an engine's tables. Any engine-private storage stays
  outside the trust boundary.
- Catalog and quota have a single source of truth in our control
  plane; in swapped-engine deployments our control plane pushes
  config into the engine (engine stores are projections, never
  sovereign).

**Payload-preserving passthrough still owns enterprise unification.**
The gateway carries the ModelCatalog aggregate (`ModelId →
Deployment(endpoint, CredentialPoolRef, allowlist)`): one gateway key
for a user, backend pools of provider keys (e.g. 10 OpenAI + 10
DeepSeek), per-request credential picked from the pool at request
time (round-robin / least-used; no cross-pool failover — provider
429s surface to the client, which owns retry). Credential pools are
references resolved through the credential proxy (AD-27 §6) — raw
keys never appear in the gateway. The request body passes unchanged
except the upstream Authorization header; streaming passes through
verbatim. Routing, credentials, quota, interception, and metering
are the gateway's own layers; payload transformation is explicitly
not.

**Storage picture.** Gateway dp: zero persistent storage (catalog
cache, soft quota state — re-pull on restart). Gateway cp: the
bundle-shared Postgres, schema-isolated. Append-only records: owned
exclusively by the Records/observability context (AD-28) via the
event backbone.

**Commodity capabilities slot to mature OSS:** authn (API keys now,
OIDC via go-oidc, enterprise OA adapter later), quota/rate-limit
execution (Redis token bucket), provider client (official openai-go —
faithful forwarding means only OpenAI-compatible passthrough is
required, not multi-provider normalization). The engine slot contract
is written above; invariants travel with our front layer under every
engine choice.

## Consequences

- Scope of "build" is minimal: forwarding core, chain SPI, event
  emission, ModelCatalog + control-plane API, bundle lifecycle.
- The gateway L1 design consumes this AD directly; boundary
  dependency lint lands with its first Go component.
- Swapped-engine deployments are a supported configuration, tested
  via the slot contract, not ad hoc.
- LiteLLM et al. remain the feature-checklist reference for the
  commodity tier; they are not dependencies.
