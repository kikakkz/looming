# Identity L1 — module design

Derived in #106 (onboarding scenario + RBAC redesign). Authority:
AD-27/AD-32; AD-35 records the model. Vocabulary: [glossary](glossary.md).

## 1. Scope

Full-featured identity for a single-organization deployment: local
mode by default, OIDC enterprise mode via the same aggregates. No
multi-tenancy, no org hierarchy (AD-35).

## 2. Model (flat RBAC)

```
Principal (kind: human | service)
  ├─ LoomingKey   (dual-track: SHA-256 hash for validation + AES-GCM
  │               sealed for repeatable reveal; masked by default;
  │               one-way revoke)
  ├─ Quota        (amount/window; engine budgets are its projection)
  └─ Roles ──► Permissions (effective set = union)
IdentityMap       (LoomingKey → engine credential REFERENCE only)
RegistrationPolicy (admin-only | invite | self-register-with-approval)
```

Builtin roles: `admin` (all permissions), `member`
(`gateway:use`, `model:use:*` of the deployment's catalog,
`quota:view`, key self-service). Custom roles: additive, later slice.

## 3. Journeys

**Onboarding** (scenario 2): visit guide page → register (per policy;
self-register → pending) or admin provisions → approval (gated by
`identity:approve`) → login → create key (raw shown at issue; masked
by default afterwards, copy-on-demand) → CLI configures local agents
→ use → `looming usage`.

**Key lifecycle**: issue (dual-track at rest — SHA-256 hash for the
validation path, AES-GCM sealed blob for the reveal path; events:
KeyIssued) → use (the gateway validates the hash from its feed
projection, never per-request here) → reveal (owner or admin,
repeatable — the sealed track exists for exactly this) → revoke
(KeyRevoked → the feed marks the key revoked → the gateway syncer
deletes it from the key cache) — revoked keys are 401
immediately-ish: propagation is bounded by the feed-watch latency
plus the gateway's positive-cache TTL.

**Engine provisioning** (Journey 2): the engine credential belongs to
the **LoomingKey**, not the principal — the key is the unit of
revocation, and two keys of one principal must be revocable
independently. Provisioning is idempotent per `(loom_key, engine)`;
revocation propagates symmetrically to that key's credential only.

## 4. Aggregates and ownership

| Aggregate | Invariants |
|---|---|
| Principal | one identity primitive; service kind carries blueprint ref; status: pending → active → disabled |
| LoomingKey | dual-track at rest (SHA-256 hash for validation, AES-GCM sealed for reveal); masked by default (prefix + last4); reveal repeatable, owner or admin; one-way revoke; per-principal rate limit on issuance |
| Quota | per-principal; window semantics owned here, execution in engine (AD-32) |
| IdentityMap | maps **LoomingKey** (not principal) → engine credential reference; no plaintext credentials (credential proxy owns those, AD-27 §6) |
| RegistrationPolicy | exactly one active policy; policy change is audited |
| Role/Permission | builtin roles immutable; custom roles are additive; permission namespace per component |

## 5. Ports

- **AuthNProvider**: builtin (local credentials, invite tokens) |
  OIDC (Keycloak-compatible; OA systems as same-contract adapters).
  Bootstrap selects; aggregates oblivious.
- **EngineProvisioner**: outbound to the engine admin channel.
- **AuditEmitter**: decision events (approvals, role changes) — records plane shapes.
- **Read side for the gateway** (slice B contract): REST + JSON over
  HTTP, same mux style as the admin/self API — not gRPC/GraphQL. Three
  endpoints, service-token guarded (`IDENTITY_GATEWAY_TOKEN`,
  `Authorization: Bearer`; bundle-internal mTLS/OAuth is the phase-2
  upgrade per #109-#112): `GET /v1/gateway/feed` returns the full
  projection (revision + keys + principals; active principals' keys
  only — disabled owners' keys vanish, fail closed; revoked keys are
  listed with their status so syncers can distinguish delete from
  never-present); `GET /v1/gateway/feed?watch=1&since_rev=N` holds in
  the Consul blocking-query shape until the in-process revision
  advances or `IDENTITY_WATCH_TIMEOUT` (30s default) elapses, then
  returns the current snapshot regardless — the response is always a
  full projection, never a delta. The revision is in-process monotonic
  and resets on restart; a response rev below the consumer's
  last-seen rev is the documented restart signal (the consumer
  refetches with `since_rev=0`, a full resync). Key issue/revoke and
  principal approve/disable/enable bump the revision hub; policy
  changes do not (policy is not in this feed). `POST
  /v1/gateway/keys/validate` resolves a raw key hash to its active
  principal; revoked, non-active-owned, and unknown keys share one
  404, which the gateway maps to an auth failure. The gateway's data
  plane never calls identity per request: its key cache is the feed's
  projection, refreshed by the watch, with the validate endpoint only
  as the cache-miss fallback. Contract formalization via OpenAPI
  arrives when a second language consumes it (AD-34 rule 3); gRPC only
  if measured transport limits demand it.

## 6. API surface (v1)

- Admin: principals CRUD + approve, roles assign, policy get/set,
  quota set, keys list/revoke (any; reveal via the self reveal route
  with an admin session), IdentityMap inspect.
- Self: register, login (provider-selected), keys issue (raw shown
  once at issue) / list (masked: id, name, prefix, last4, status,
  created_at) / get (masked) / reveal (owner or admin, repeatable) /
  revoke (own; one-way, 409 on a repeat), effective permissions view,
  quota view.
- Bootstrap (first admin, topology-l1 §4): `POST /v1/bootstrap/invite`
  `{email}` → 201 `{token, expires_at, invite_url_path}` — mints the
  one-time invite for the deployment's pre-selected
  `initial_admin_email`; `looming-ctl apply` prints the raw token once
  (PR-B) and the operator carries it to the mailbox. Guarded by
  `Authorization: Bootstrap <IDENTITY_BOOTSTRAP_KEY>` (constant-time;
  unconfigured key → 503 `bootstrap_disabled`, wrong key → 401).
  One-shot window as domain rules: enabled only while no principal
  with role `admin` exists AND no bootstrap-sourced invite has ever
  been created; otherwise 409 `bootstrap_closed`. The invite is stored
  with `source='bootstrap'` and bound to the declared email.
  Registration presenting it bypasses the RegistrationPolicy mode
  entirely (the first admin registers even under admin-only — the env
  `BOOTSTRAP_ADMIN_USERNAME/PASSWORD` mechanism is retired), requires
  the bound email (mismatch → 400), consumes one-time as any invite,
  and lands the principal active with roles `[admin, member]` (member
  keeps key self-service working per §2).
- Gateway-facing: key validate (hash + status) — plus the feed
  snapshot and blocking watch described in §5. Effective permissions
  for (principal) and IdentityMap resolve remain the two further seams
  the data plane consumes (slices D and C respectively).

## 7. Aspects

PEP (permission checks execute at the gateway, data lives here);
identity propagation (Looming key northbound only — unchanged);
audit emission (registration/role/quota decisions); revisions and
idempotency (key issuance idempotency keys, provisioning idempotent
per (principal, engine) — both from slice-0 patterns).

## 8. Deferred (named triggers)

- Custom roles beyond builtin admin/member — trigger: first org whose
  team structure exceeds two roles (per the maintainer: use roles, or
  deploy more instances).
- Per-key model scopes — trigger: a real user needing key-level
  restriction beyond their roles.
- Groups/teams as a collection primitive — trigger: role assignment
  pain at tens of users.
- Multi-tenancy — decided non-goal (AD-35): if ever, RBAC scoping
  dimension + tenant_id schema evolution; multiple deployments remain
  valid permanently.
