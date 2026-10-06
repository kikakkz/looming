-- identity slice C: engine-credential bookkeeping per LoomingKey
-- (identity-l1 §3/§4). The map is REFERENCE-only: credential_ref names
-- the engine-side credential; credential_enc is the AES-GCM sealed
-- value (the LoomingKey dual-track precedent — plaintext credentials
-- never enter this context, AD-35). Provisioning is idempotent per
-- (key, engine); revocation deletes the row.

CREATE TABLE identity_map (
    key_id         uuid NOT NULL REFERENCES loom_keys (id) ON DELETE CASCADE,
    engine         text NOT NULL,
    credential_ref text NOT NULL,
    credential_enc bytea NOT NULL,
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (key_id, engine)
);

-- Revocation and quota propagation look entries up by key.
CREATE INDEX identity_map_key_idx ON identity_map (key_id);
