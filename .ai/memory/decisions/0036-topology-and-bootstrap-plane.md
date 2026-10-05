---
number: 36
title: "topology and bootstrap plane"
date: "2026-10-05"
updated: "2026-10-05"
status: "accepted"
supersedes: []
adopted-at: "2026-10-05"
---

# AD-36 — topology and bootstrap plane

## Context

#107 (multi-host first wave) needs the bootstrap/topology plane
designed: how a bundle is installed and extended with hosts, how
components are placed and supervised, and how the first admin and
end users get onboarded. The maintainer discussion surveyed the
bootstrap UX of mature self-hosted products and settled eight
questions: config UX, desired-vs-observed state, host join, the
runtime plane, Postgres layout, first admin, the guide page, and
consensus. Survey evidence: GitLab omnibus (single config file +
`gitlab-ctl reconfigure`, idempotent converge), k3s (persisted
declarative config), k0sctl/chef (desired-state convergence),
kubeadm bootstrap tokens (one-time, TTL'd, hash-stored), Gitea
(first registrant becomes admin), Nextcloud/Gitea web wizards
(end-user audience — rejected for admins), Argo (secret retrieval —
rejected), GitLab/Zulip/Sourcegraph (database-per-component with
separate roles), Vault-init-style secret files (rejected), Patroni
(Postgres-ecosystem HA appliance).

## Decision

1. **Config UX**: a single `/etc/looming/topology.yaml` plus an
   idempotent `looming-ctl apply` (GitLab reconfigure / k3s
   persisted-config precedent); render-diff restarts only what
   changed; NO web wizard — the audience is admins.
2. **Desired vs observed state**: the YAML file is desired state
   (hand-editable, versionable); the Postgres `topology` database is
   observed/runtime state; converge reconciles the two (k0sctl/chef
   shape).
3. **Join**: pull-based, kubeadm-family bootstrap tokens — one-time,
   TTL default 24h, hash-stored, role-scoped, revocable/rotatable;
   `looming-ctl join` self-registers the host; no SSH/agentless push
   (k0sctl/Ansible rejected: no continuous reconciliation, and an
   inbound-SSH dependency); anti-MITM cert-pinning is deferred with
   #110 TLS.
4. **Runtime plane**: Docker containers for ALL components — one
   runtime plane shared with the sandbox plane; per-host compose
   files rendered by converge; `restart: unless-stopped` as the
   liveness floor; ctl wraps docker commands; NO systemd-unit
   rendering, NO embedded supervisor (runit/s6); a k3s backend only
   when sandbox elasticity demands it (the maintainer's stated
   trigger).
5. **Postgres topology**: the bundle owns its PG instance;
   database-per-component with separate roles (GitLab/Zulip/
   Sourcegraph consensus; schema-per-app rejected at this scale);
   external PG is the escape hatch; backup is pg_dump-based with
   config/secrets backed up separately, cron-scheduled outside the
   app (AD-30-compliant).
6. **First admin**: pre-selected `initial_admin_email` plus a
   one-time invite token printed by `apply` (Gitea bloodline
   hardened against self-registration races); NO pre-seeded
   credentials, NO Vault-init-style secret files (GitLab's
   `initial_root_password` explicitly rejected by the maintainer);
   the identityd `BOOTSTRAP_ADMIN_USERNAME/PASSWORD` env mechanism
   retires when this lands.
7. **Guide page**: a public, unauthenticated onboarding page for
   END USERS (download CLI / endpoints / register or ask an admin),
   zero credentials by invariant; `access.public` default true
   (off = invite-only orgs); bootstrap admin docs live in the repo,
   not on the page.
8. **Consensus**: no raft/gossip in the application architecture at
   any host count — all writes converge to Postgres, conflicts are
   arbitrated by transactions/constraints; HA = stateless gateway
   replicas (#109) + DB-level replication; Patroni-style consensus
   is PG-internals only, never an architecture decision.

## Consequences

- The design lands as `docs/architecture/topology-l1.md` (L1 module
  design); implementation slices T0–T3 (component skeleton, apply
  converge, pull-join + tokens, guide render + gateway public route)
  are filed as issues after the design PR merges; the gateway public
  guide route is slice T3.
- #114 schema wording note: each component gets its own DATABASE,
  not a schema, in the bundle Postgres — wording drift in docs is
  corrected to match (AD-29-era "schema-isolated" phrasing included).
- The Records context later owns the event model for bootstrap-time
  audit (topology changes, token mint/consume); until then the
  topology context writes a local append-only audit table.
