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
  ├─ LoomingKey   (hash at rest; issued plaintext shown once; one-way revoke)
  ├─ Quota        (amount/window; engine budgets are its projection)
  └─ Roles ──► Permissions (effective set = union)
IdentityMap       (principal/key → engine credential REFERENCE only)
RegistrationPolicy (admin-only | invite | self-register-with-approval)
```

Builtin roles: `admin` (all permissions), `member`
(`gateway:use`, `model:use:*` of the deployment's catalog,
`quota:view`, key self-service). Custom roles: additive, later slice.

## 3. Journeys

**Onboarding** (scenario 2): visit guide page → register (per policy;
self-register → pending) or admin provisions → approval (gated by
`identity:approve`) → login → create key (plaintext once) → CLI
configures local agents → use → `looming usage`.

**Key lifecycle**: issue (hash stored, events: KeyIssued) → use
(gateway validates hash, checks effective permissions) → revoke
(KeyRevoked → gateway cache purge + engine credential revocation via
IdentityMap) — revoked keys are 401 immediately-ish (cache event +
TTL bound).

**Engine provisioning** (Journey 2): KeyIssued/approved → provision
engine credential via the adapter (EnginePlane.Admin) → IdentityMap
records the reference. Revocation propagates symmetrically.

## 4. Aggregates and ownership

| Aggregate | Invariants |
|---|---|
| Principal | one identity primitive; service kind carries blueprint ref; status: pending → active → disabled |
| LoomingKey | hash-only at rest; prefix + checksum for typo detection; one-way revoke; per-principal rate limit on issuance |
| Quota | per-principal; window semantics owned here, execution in engine (AD-32) |
| IdentityMap | references only — no plaintext credentials (credential proxy owns those, AD-27 §6) |
| RegistrationPolicy | exactly one active policy; policy change is audited |
| Role/Permission | builtin roles immutable; custom roles are additive; permission namespace per component |

## 5. Ports

- **AuthNProvider**: builtin (local credentials, invite tokens) |
  OIDC (Keycloak-compatible; OA systems as same-contract adapters).
  Bootstrap selects; aggregates oblivious.
- **EngineProvisioner**: outbound to the engine admin channel.
- **AuditEmitter**: decision events (approvals, role changes) — records plane shapes.
- Read side for the gateway: effective-permissions cache feed
  (event-invalidated, single-writer cache pattern from slice 1).

## 6. API surface (v1)

- Admin: principals CRUD + approve, roles assign, policy get/set,
  quota set, keys list/revoke (any), IdentityMap inspect.
- Self: register, login (provider-selected), keys issue/list/revoke
  (own), effective permissions view, quota view.
- Gateway-facing: key validate (hash + status), effective permissions
  for (principal), IdentityMap resolve — the three seams the data
  plane consumes.

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
