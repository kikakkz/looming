# Topology L1 — module design

Derived in #107 (multi-host first wave). Authority: AD-27/AD-28
(components/contexts), AD-34 (polyglot layout), AD-36 (this design's
decisions). Vocabulary: [glossary](glossary.md).

## 1. Scope

Single-organization bundle first-boot plus topology management.
Independent of the Orchestration context (blueprints/runs, → #4).
Phase-1 envelope per the AD-35-era phase decisions: single gateway
front, HTTP, plain IPs, trusted network. Phase-2 items live ONLY in
#109 (gateway HA), #110 (auto TLS), #111 (DNS), #112 (cross-NAT) —
this design references them, never duplicates them.

## 2. Model

```
Topology (aggregate root; desired state snapshot)
  ├─ Host        (registered machine: id, address, role labels, joined_at)
  ├─ ComponentPlacement (component × host, port/config overrides; gateway front exactly 1 in phase-1)
  ├─ JoinToken   (one-time, TTL default 24h, hash-stored, role-scoped, revocable — kubeadm shape)
  └─ Guide       (rendered artifact from Topology snapshot; idempotent re-render)
```

**Supervision shape.** Every component runs as a container on Docker —
the single runtime plane (the sandbox plane already reserves Docker;
one plane for everything). Per-host compose files are the unit of
deployment; `restart: unless-stopped` is the liveness floor;
`looming-ctl status/restart/tail` wraps docker commands. NO
systemd-unit rendering, NO embedded supervisor (runit/s6) — the
earlier VM-native systemd variant was explicitly rejected by the
maintainer.

**Consensus note.** Raft/gossip have no legitimate place in the
application architecture at ANY host count: all fact-writes converge
to Postgres and conflicts are arbitrated by transactions/constraints;
HA is stateless-replica redundancy for the gateway (#109) plus
DB-level replication for Postgres. The only indirect contact with
consensus is if a Patroni-style PG-HA appliance is chosen later —
that is Postgres-ecosystem internals, not an architecture decision.

## 3. Journeys

**Admin bootstrap**: install the bundle on the first host → write
`/etc/looming/topology.yaml` → `looming-ctl apply` (converge:
validate invariants → bring up the state plane: start and initialize
the bundle-owned Postgres, creating the `topology` database — on
first boot it is started before any write → persist the topology →
render per-host compose files → `docker compose up -d` on each
declared host — restart only what changed; prints the invite link
plus the join hint).

**Adding a host**: `looming-ctl token create --role engine --ttl 24h`
→ operator runs on the new host: install bundle →
`looming-ctl join http://<first-host>:<port> --token <t>` → the host
registers itself (pull; no SSH) and its persistent credential lands at
`/etc/looming/host.cred` (0600; the plaintext was shown exactly once
at join). Re-join / address-or-label refresh presents that credential:
`Authorization: Host <host-id>:<credential>` on
`POST /v1/join/rejoin`. Joining an address another host already holds
without its credential fails 409 — a second credential is never
minted; recovery is re-running join on the original host (its
credential file is intact) or admin SQL against the topology database.

**End-user onboarding**: visit the gateway URL → public guide page
(unauthenticated; download-CLI link, identity/gateway endpoints,
register-or-ask-admin steps) → CLI configures local agents. The CLI
itself is #108's design surface — this doc only names the contract
values the guide renders: `http://<static-ip>:<port>` endpoints in
phase-1.

## 4. First-admin mechanism

Bootstrap config declares `bootstrap.admin_email`; `looming-ctl apply`
mints a ONE-TIME invite token for that email (reusing identity's
existing invite mechanism — identity-l1 §5) and prints the invite
link to the terminal; the operator copy-pastes it to the mailbox.
The email holder registers → active + admin role automatically.
Bloodline: Gitea-style "first registrant becomes admin" (no
pre-seeded credentials anywhere), hardened with the pre-selected
email against self-registration races. Consequence: the identityd
`BOOTSTRAP_ADMIN_USERNAME/PASSWORD` env mechanism is retired — the
bootstrap-invite endpoint (identity-l1 §6) is the only first-admin
path, and `IDENTITY_BOOTSTRAP_KEY` is the only bootstrap-scoped
secret.

## 5. Aggregates and invariants

| Aggregate | Invariants |
|---|---|
| Topology | hosts ≥ 1; placements reference registered hosts; gateway-front placement exactly 1 (#109 lifts to N); `access` accepts only direct/ip/http in phase-1 — vip/dns/acme shapes are reserved in the schema but rejected at apply, with the owning phase-2 issue named (#109–#112); optimistic revision (component-patterns #3) |
| Host | address unique; join is pull-based self-registration — a successful join mints the host's persistent service credential (its AD-27 §6 service-subject identity; re-join authenticates with that credential, identifies the host by id, and updates address/labels); no heartbeat in the aggregate (liveness is the supervisor's concern — the restart policy — not topology's) |
| JoinToken | one-time consume; TTL default 24h; hash at rest; atomic consume (guarded UPDATE — the slice-A invite-token precedent) |
| ComponentPlacement | (component, host) unique; port conflicts rejected at apply |
| Guide | render input = the current Topology snapshot; regenerated on every apply; served only when `access.public`; zero credentials by invariant |

## 6. Ports

- **TopologyStore**: Postgres, `topology` database — database-per-component per AD-36.
- **JoinAuthorizer**: token mint/consume.
- **Renderer**: topology → per-host compose files; docker backend in
  phase-1; the backend interface leaves room for a future k3s backend
  (trigger: sandbox-pool elasticity).
- **GuidePublisher**: the gateway fetches the rendered guide over
  internal HTTP — the same pattern as the identity feed (REST+JSON,
  service token).

## 7. API surface (v1)

`looming-ctl` admin commands: `apply` (with `--print-invite` to force
the bootstrap-invite request), `token create|list|revoke` (admin side,
direct to the topology DB), `join <first-host-url> --token <t>` (the
pulling host), `status`, `render` (dry-run compose output),
`guide show`. The CLI binary's own component placement is #108's
design surface — this doc only fixes the admin-side command contract.

The join/rejoin endpoints are served by **topologyd**, the topology
component's long-running service binary (house naming: gateway →
gateway, identity → identityd). Its placement is declared in the
topology YAML like any other component's — typically the state host,
never forced there:

- `POST /v1/join` `{token, host: {id?, address, labels}}` →
  `201 {host_id, credential, cluster: {access}}`: validates and
  atomically consumes the one-time token, registers the host (server
  id `host-<uuid8>` when `id` is absent), mints the host's persistent
  credential (plaintext exactly once), and answers with the gateway
  access hint. Errors: `403 token_invalid|token_expired`,
  `409 token_used|address_taken|host_conflict` (with the re-join
  recovery hint), `400 invalid_request`.
- `POST /v1/join/rejoin` with `Authorization: Host <host-id>:<credential>`,
  body `{address?, labels?}` → `200`: the credential-authenticated
  address/label refresh. Wrong credential and unknown host are the
  same `401 unauthenticated`.

Gateway guide route: `GET /` public page when `access.public` (served
from the cached guide fetch; 404 when off).

## 8. Aspects

PEP — the join endpoint is the only public inbound surface,
token-authenticated; `apply` is a local root operation (no remote
bootstrap API in phase-1). Identity propagation — joined hosts act
as service-subjects (AD-27 §6 S2S shape). Audit — topology changes,
token mint/consume → local append-only audit table; the Records
context takes over the event model later. Revisions/idempotency —
apply is converge-idempotent (render-diff, restart only changed);
join idempotent per host (a JoinToken is one-time and bootstraps only
a NEW host; re-join presents the host's persistent service
credential, identifies the host by id, and updates the
address/labels); optimistic revision on Topology (component-patterns
#3). Metering: none. ACL
trimming: N/A (no user data; the guide carries zero credentials by
invariant).

## 9. Deferred (named triggers)

- k3s/container-orchestrator backend — trigger: sandbox
  hot-pool/elasticity (the maintainer's own stated line).
- Remote/HA bootstrap API — trigger: #109 multi-replica control
  plane.
- Admin web UI — trigger: ops team demand.
- DNS/TLS/HA/cross-NAT → #109–#112 by reference.

## 10. Implementation slices (filed as issues after this design lands)

- **T0** — topology component skeleton: Go component `topology/` per
  AD-34, four-package layout, Postgres migrations, ctl cmd skeleton.
- **T1** — apply converge: YAML → validate → DB → render.
- **T2** — pull-join + tokens.
- **T3** — guide render + gateway public route.
