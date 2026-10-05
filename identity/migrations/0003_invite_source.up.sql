-- identity: invite provenance for the first-admin mechanism
-- (topology-l1 §4, T2 PR-A). source tags which path minted a voucher;
-- email binds a bootstrap invite to its declared address. Existing rows
-- are admin invites by construction (the bootstrap endpoint does not
-- exist yet), so 'admin' is the backfill default.
ALTER TABLE invite_tokens ADD COLUMN source text NOT NULL DEFAULT 'admin'
    CHECK (source IN ('admin', 'bootstrap'));
ALTER TABLE invite_tokens ADD COLUMN email text NOT NULL DEFAULT '';
-- The one-shot window is database-enforced, not just app-checked: two
-- concurrent mints can both pass the read-only window check, so the
-- losing insert must fail here (mapped to 409 bootstrap_closed).
CREATE UNIQUE INDEX invite_tokens_bootstrap_source_uidx ON invite_tokens (source)
    WHERE source = 'bootstrap';
