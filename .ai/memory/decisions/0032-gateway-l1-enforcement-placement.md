---
number: 32
title: "gateway L1 enforcement placement"
date: "2026-10-03"
updated: "2026-10-03"
status: "accepted"
supersedes: []
adopted-at: "2026-10-03"
---

# AD-32 — gateway L1 enforcement placement

## Context

The gateway L1 derivation (#89, the first domain-design instance)
refined where each responsibility physically lives. AD-29 fixed the
slot-host shape but left enforcement placement coarse: it had the
front layer doing catalog-driven routing and quota enforcement. The
discussion settled a sharper split, on two principles the maintainer
stated directly: the architecture owns contracts and the unified read
surface while instances own mechanics (so mature mechanisms get
reused instead of rebuilt); and governance-class semantics must not
vary with whatever engine is plugged (so authorization stays at the
uniform layer, while budget mechanics may vary).

## Decision

Three amendments to AD-29, plus two clarifications:

1. **Quota execution lives in the gateway instance** (engine-native
   budgets), configured with policy numbers from the Identity &
   access Quota aggregate at provisioning time. The architecture owns
   the unified display via MeterRecords — not a second source of
   truth, per AD-29's existing rule. AD-29's swap-invariance narrows:
   quota semantics excepted. Quota *adjustment* after provisioning is
   deferred (named mounting point, not silently dropped).
2. **The front layer does not route.** Model id → provider endpoint
   and credential-pool selection are engine-instance config
   (LiteLLM `model_list` and peers); the front layer passes model ids
   through untouched. Looming holds a read projection of that config
   in the gateway cp for provisioning and display — never a routing
   authority.
3. **Model permission is enforced at the front layer, fail-closed**
   (ModelAllowed between Authenticate and the interception chain).
   Engines are configured to pass through; per-user engine
   credentials exist for quota attribution only and carry no
   permission semantics. Permission policy data originates from
   Identity & access / Registry and is cached in the gateway cp.
4. **Identity is one Looming-facing authority plus provisioning into
   the engine**: users hold one Looming key; provisioning creates the
   mapped engine credential (IdentityMap, owned by the identity
   context, cached read-only in the gateway); the front layer
   injects the per-user engine credential per request. Not-provisioned
   is a normal observable state (SCIM semantics), distinct from
   key-not-found only southbound.
5. **Metering counts only calls that reached a model.** Denials
   (authn, permission, interception chain) carry no tokens and live
   exclusively in the audit DecisionEvent stream; a discrepancy
   between MeterRecords and engine-side usage is an
   implementation-bug signal, never a display normalization.

The engine slot contract gains two named faces: `EnginePlane.Forward`
(the engine's northbound OpenAI-compatible endpoint — plain
proxying) and `EnginePlane.Admin` (ProvisionKey / SetBudget /
RevokeKey / GetUsage — LiteLLM maps to its native `/key/*` REST; the
default thin implementation implements it against its own config
store; engines without admin capability are forward-only).

## Consequences

- AD-29 stands except where narrowed above; the storage picture
  (stateless dp, bundle Postgres for cp, records owned by the
  Records context) is unchanged.
- The gateway context owns less than AD-29 implied: no routing, no
  quota execution, no quota-policy authority — and owns one thing
  AD-29 under-specified: model-permission enforcement.
- v0.1 scope: provisioning sets a budget once and runs the flow;
  quota adjustment journeys and engine-side drift reconciliation are
  deferred with named mounting points in the L1 design.
- Boundary dependency lint (day one) gets concrete rules: dp may not
  write projections; the only engine write path is EnginePlane.Admin;
  the only identity write path is the identity context's API.
