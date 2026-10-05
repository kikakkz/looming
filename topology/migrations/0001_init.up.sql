-- topology slice T0 (#107): full phase-1 schema (topology-l1 §5 aggregates).
-- The topology database itself is bundle-Postgres, created by the state
-- plane at first boot (T1); these are the relations inside it.

-- Desired-state snapshot: exactly one row by fixed PK, so the
-- singleton invariant holds structurally. Uninitialized (zero rows) is
-- a real state: Store.Current maps it to domain.ErrNoTopology. The
-- full access triple is persisted — not just the mode — because
-- converge-idempotency compares the reloaded value against the next
-- declare: a mode-only row would make every fresh-process apply bump
-- the revision.
CREATE TABLE topology (
    id              text PRIMARY KEY,
    access_mode     text NOT NULL,
    access_transport text NOT NULL,
    access_endpoint text NOT NULL,
    revision        bigint NOT NULL DEFAULT 1,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Registered machines. address unique is the Host aggregate's uniqueness
-- invariant; credential_hash is the persistent re-join service credential
-- slot (topology-l1 §5 Host row) — NULL until T2's join mints it.
CREATE TABLE hosts (
    id              uuid PRIMARY KEY,
    address         text UNIQUE NOT NULL,
    role_labels     text[] NOT NULL DEFAULT '{}',
    credential_hash bytea NULL,
    joined_at       timestamptz NOT NULL DEFAULT now()
);

-- Component placements: the Topology's desired placement set, replaced
-- wholesale per successful declare (converge). UNIQUE(component, host_id)
-- is the placement uniqueness invariant, enforced in app and schema.
CREATE TABLE placements (
    id         uuid PRIMARY KEY,
    component  text NOT NULL,
    host_id    uuid NOT NULL REFERENCES hosts (id),
    config     jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (component, host_id)
);

-- Join tokens (kubeadm shape: one-time, TTL'd, hash-stored, role-scoped;
-- topology-l1 §5 JoinToken row). The table lands with T0 so the schema
-- is complete in one migration; the aggregate and its mint/consume
-- service are T2's work.
CREATE TABLE join_tokens (
    token_hash bytea PRIMARY KEY,
    role       text NOT NULL,
    created_by text NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz NULL
);

-- Rendered onboarding guide: the artifact T3 regenerates on every apply
-- and the gateway serves at its public route. Zero credentials by
-- invariant; snapshot is the Topology snapshot it was rendered from.
CREATE TABLE guide (
    id          text PRIMARY KEY,
    snapshot    jsonb NOT NULL,
    rendered_rev bigint NOT NULL,
    rendered_at timestamptz NOT NULL DEFAULT now()
);
