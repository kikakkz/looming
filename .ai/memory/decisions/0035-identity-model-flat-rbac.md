---
number: 35
title: "identity model — flat RBAC"
date: "2026-10-04"
updated: "2026-10-04"
status: "accepted"
supersedes: []
adopted-at: "2026-10-04"
---

# AD-35 — identity model — flat RBAC

## Context

The onboarding scenario (#106 discussion, 2026-10-04) requires identity
built full-featured now, without multi-tenancy or an organization
hierarchy. The maintainer's direction: flat users plus RBAC; teams are
expressed with roles, and hard isolation is expressed by deploying
multiple instances. ApiKey/Quota remain self-authored platform
aggregates (no mature off-the-shelf exists); user authentication is a
pluggable port — builtin local mode ships as the bundle default and a
Keycloak-compatible OIDC adapter ships in the same build, not later.
Bootstrap's access simplifications (single gateway, HTTP, plain IP,
trusted network; #107) defer four items with named triggers: #109 (HA),
#110 (automated TLS), #111 (DNS), #112 (cross-NAT connectivity).

## Decision

The identity context is a flat RBAC system in the NIST/Kubernetes
shape:

- **Principal** — the only identity primitive; one table,
  `kind: human | service`. Service principals carry their owning
  blueprint reference and are audited in the Agent 365 S2S subject
  shape (AD-27/AD-32). Multi-tenant isolation is intentionally absent:
  tenants are separate deployments.
- **Role** — named permission bundles. Builtin: `admin`, `member`;
  custom roles are additive later. Role assignment is per-principal.
- **Permission** — fine-grained strings composed by roles:
  `gateway:use`, `model:use:<model-id>` (wildcard
  `model:use:*`), `identity:approve`, `identity:admin`; future
  components extend the same namespace (`memory:read`,
  `registry:admit`, …).
- **LoomingKey** — belongs to a principal; only a hash is stored
  (plaintext shown once at issuance); active → revoked is one-way.
- **Quota** — per-principal resource policy, separate from
  authorization; engine-side budgets are its projection (AD-32).
- **IdentityMap** — principal/key → engine credential reference only;
  plaintext credentials never enter this context (credential proxy).
- **RegistrationPolicy** — per-deployment: admin-only | invite |
  self-register-with-approval (self-register produces a pending
  principal; approval is gated by `identity:approve`).
- **AuthNProvider port** — builtin or OIDC adapter selected by
  bootstrap config; aggregates are unchanged by the choice.

## Consequences

- The gateway's model-permission check consumes effective permissions
  (roles → union), replacing the earlier per-subject allowlist draft;
  enforcement stays at the front layer, fail-closed (AD-32), fed by the
  identity cache (slice-1 seam).
- Future multi-tenancy (if ever) is a scoping dimension on this spine —
  the Kubernetes Role/ClusterRole precedent — plus a `tenant_id`
  schema evolution; it is a decided non-goal for now. Multiple
  deployments remain a permanently valid tenant boundary.
- Events (UserProvisioned / KeyIssued / KeyRevoked / QuotaChanged /
  RoleChanged) drive gateway cache invalidation and engine
  provisioning (Journey 2).
- The bootstrap design (#107) references its deferred phase-2 items by
  issue number: #109, #110, #111, #112.
