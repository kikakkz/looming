-- identity slice B: LoomingKey dual-track storage (identity-l1 §2).

CREATE TABLE loom_keys (
    id           uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES principals (id),
    name         text NOT NULL DEFAULT '',
    prefix       text NOT NULL,
    last4        text NOT NULL,
    key_hash     bytea NOT NULL UNIQUE,
    key_enc      bytea NOT NULL,
    status       text NOT NULL DEFAULT 'active',
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz NULL
);

-- The issuance rate limit counts recent keys per principal.
CREATE INDEX loom_keys_principal_created_idx ON loom_keys (principal_id, created_at);
