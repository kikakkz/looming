-- identity slice A: core schema (identity-l1 §4 aggregates).

CREATE TABLE principals (
    id            uuid PRIMARY KEY,
    username      text UNIQUE NOT NULL,
    kind          text NOT NULL,
    display_name  text NOT NULL DEFAULT '',
    password_hash text NULL,
    status        text NOT NULL,
    roles         text[] NOT NULL DEFAULT '{member}',
    version       bigint NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Exactly-one-active holds structurally: one row by fixed PK.
-- Seeded with the most restrictive mode so a fresh deployment never
-- opens registration by accident.
CREATE TABLE registration_policy (
    id         text PRIMARY KEY,
    mode       text NOT NULL,
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO registration_policy (id, mode, updated_by)
VALUES ('singleton', 'admin-only', 'system');

-- Session tokens: hash-at-rest, one-way revoke.
CREATE TABLE authn_tokens (
    token_hash  bytea PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES principals (id),
    issued_at   timestamptz NOT NULL,
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz NULL
);

CREATE INDEX authn_tokens_principal_idx ON authn_tokens (principal_id);

-- Registration vouchers: hash-at-rest, single-use.
CREATE TABLE invite_tokens (
    token_hash bytea PRIMARY KEY,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz NULL
);
