# Gateway L1 — module design

The first L1 under the domain-design method, derived in #89 (two
settled rounds + a verification storm). Authority: AD-27 §5, AD-28,
AD-29 as narrowed by AD-32. Vocabulary: [glossary](glossary.md).

## 1. Scope

One gateway context, one engine slot per deployment (no concurrent
engines). v0.1 engines: the default thin implementation and LiteLLM.

## 2. Boundaries (from AD-28 as amended by AD-32)

| The gateway owns | The gateway does not own |
|---|---|
| Authn fan-in (Looming key validation) | Routing config (engine instance) |
| Model-permission enforcement (fail-closed) | Quota execution (engine instance) |
| Interception chain (risk control, jev/laya links) | Quota policy, IdentityMap authority (Identity & access) |
| IdentityMap read cache; engine-config read projection | MeterRecord / DecisionEvent stores (Records) |
| EnginePlane.Admin channel (provisioning operations) | Model/policy governance data (Registry / Identity) |
| In-band MeterRecord; async InteractionBody emission | |

## 3. Discovery — the two journeys

Full command/event/policy walk with the seven storm rulings: #89's
event-storm comment and verification-storm record (2026-10-03). In
command order, journey 1 (request):

```
Authenticate ⚡ → ModelAllowed ⚡ → ResolveIdentity (cache, miss→authority)
→ ExchangeCredential ⚡ (cached, TTL) → RunInterceptionChain ⚡
→ forward (payload-preserving, model id untouched) → Meter (inline)
→ LogInteraction (async, assembled at stream end, capped, truncated flag)
```

Rulings folded in: exchange failure splits 401 (revoked) vs 503 +
Retry-After (fault); denials emit DecisionEvents only (never
MeterRecords); SSE assembled at completion under a size cap with no
back-pressure; identity cache is cache-aside with event invalidation
and miss call-through. Journey 2 (provisioning):

```
CreateKey → LoomingKeyIssued → ProvisionEngineKey 〖EnginePlane.Admin〗
→ EngineKeyProvisioned | ProvisionFailed (bounded retry + explicit event)
→ IdentityMap entry (identity ctx) active
```

Not-provisioned is a first-class observable state; the gateway fails
closed (northbound 401 identical to key-not-found; southbound events
differ: KeyNotFound / KeyNotProvisioned / KeyRevoked).

## 4. Aggregates and ownership

| Aggregate / data | Owner | Gateway's shape of it |
|---|---|---|
| User, LoomingKey, Quota, **IdentityMap** | Identity & access | read-only cache (event-invalidated, ~30s TTL, miss call-through) |
| Route config, engine credential pools | Engine instance | read projection (cp) + Admin write channel |
| MeterRecord, InteractionBody, DecisionEvent | Records/observability | emit-only, our shapes |
| Permission policy (model allowlists) | Identity & access / Registry | read cache, enforced at the front layer |

Slice B wires the first half of this table for real: the control
plane's KeyCache (single-writer, revision-monotonic — the
component-patterns #2 discipline) is the LoomingKey projection, fed by
a Syncer over identity's blocking feed (full snapshot at boot, then
long-poll diffs; rev-rollback resets wholesale). The
IdentityAuthenticator authorizes off the cache and falls back to
identity's validate endpoint on a miss, confirming positives for a
30s TTL — the data plane never calls identity per request, and
revocation is 401 immediately-ish (watch latency + TTL bound).
ModelAllowlistCache remains the unwired slice-D seam for the model
permission check.

Slice C consumes the feed's per-key `engine_credential` (identity
PR #125/#126): the KeyCache projection carries it per row, the
IdentityAuthenticator returns `Identity{Subject, EngineCredential}`
(the front-port Authenticator's house shape), and the front hands the
engine slot ONLY the credential — the LoomingKey is deleted at the
authn boundary and never enters the engine call path (§6's "Looming
key northbound only, engine credential southbound only", now enforced
in code). `EnginePlane.Forward` takes the credential as an explicit
parameter; the default engine injects it as the per-request upstream
`Authorization`, falling back to the configured static upstream auth,
falling back to stripping the header. Identity's validate endpoint
answers (principal, status) only — an origin fallback therefore
authorizes with an empty credential and the feed fills the value on its
next sync (bounded by the watch latency); confirms never overwrite a
feed-projected credential. The transcript recorder carries bodies only
— no header fields exist on InteractionBody — so neither the LoomingKey
nor any engine credential can land in a captured record by
construction, and the invariant is pinned by assertion tests both
ways.

## 5. Modules

- **gateway/dp** — the front layer: authn, ModelAllowed, chain,
  identity/credential resolution, payload-preserving forward,
  metering emission. Stateless.
- **gateway/cp** — config projection cache, IdentityMap cache,
  provisioning operations (the only writer of engine-side state via
  EnginePlane.Admin; the only reader of identity authority via its
  API). Bundle Postgres with its own database (database-per-component,
  AD-36).
- **gateway/engine** — the engine slot: `EnginePlane.Forward` +
  `EnginePlane.Admin` interfaces; the default thin implementation
  (own route config + credential pool in its own store); the LiteLLM
  adapter (`/key/*` mapping per AD-32).

Boundary lint rules (day one): dp must not import engine internals —
only EnginePlane interfaces; the only engine write path is
EnginePlane.Admin; the only identity write path is the identity
context API; nothing outside records plane writes MeterRecord.

## 6. Aspects

| Aspect | Mounting point |
|---|---|
| PEP (fail-closed) | chain semantics + ModelAllowed (both fail-closed by contract) |
| Identity propagation | Looming key northbound only; engine credential southbound only |
| Audit emission | chain denials, permission denials, provision results → DecisionEvent |
| Metering | inline in dp; usage-only (AD-32 #5) |
| Event transport | backbone per AD-27 §1 |
| Revisions / idempotency | provisioning commands idempotent by (key, engine); Admin ops carry idempotency keys |

## 7. Deferred (named, not dropped)

- Quota adjustment after provisioning (sync journey, failure/retry
  semantics) — mounts on Journey 2's command set.
- Engine-side drift reconciliation (credentials existing outside
  IdentityMap) — mounts on Admin.GetUsage + a records-plane auditor.
- Forward-only engines (no Admin capability) — contract allows;
  provisioning/quota features degrade by design.

## 8. D5 unfreeze criteria

go.mod + the three modules above as a hexagonal skeleton (ports =
EnginePlane interfaces, adapters = default engine + LiteLLM) +
boundary dependency lint in CI + the Go CI job.
