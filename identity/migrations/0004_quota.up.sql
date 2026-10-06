-- identity slice C: per-principal resource policy (identity-l1 §4).
-- One row per principal; the row's ABSENCE is the unlimited default,
-- so deletion is not part of the surface. amount 0 is a deliberate
-- blocked state, not a missing row.

CREATE TABLE quota (
    principal_id uuid PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    amount       bigint NOT NULL CHECK (amount >= 0),
    unit         text NOT NULL CHECK (unit IN ('tokens', 'usd')),
    window_days  integer NOT NULL CHECK (window_days IN (1, 7, 30, 90)),
    updated_by   text NOT NULL DEFAULT '',
    updated_at   timestamptz NOT NULL DEFAULT now()
);
