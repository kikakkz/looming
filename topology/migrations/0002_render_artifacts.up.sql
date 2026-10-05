-- topology slice T1 (#107): rendered-artifact store for render-diff
-- convergence. One row per (host, kind): the last compose file the
-- converge pipeline rendered for a host, keyed by its content hash.
-- host_id is text, not uuid: host ids are operator-chosen strings from
-- topology.yaml, and T0's hosts.id uuid column is not retrofitted —
-- this table is the operator-facing key space.

-- Rendered artifacts: the per-host compose file last shipped to the
-- executor. Persisting content + hash makes apply's render-diff a
-- pure-DB compare (unchanged hash = skip entirely, no docker call)
-- and keeps the converged unit of deployment inspectable.
CREATE TABLE render_artifacts (
    host_id      text NOT NULL,
    kind         text NOT NULL,
    content_hash text NOT NULL,
    content      text NOT NULL,
    rendered_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (host_id, kind)
);
