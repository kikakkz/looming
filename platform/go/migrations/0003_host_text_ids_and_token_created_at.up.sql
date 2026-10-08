-- topology slice T2 (#107): two real gaps in the T0 schema discovered
-- while landing pull-join.
--
-- 1) hosts.id is uuid, but joined hosts mint human-prefixed identities
--    (host-<uuid8>, topology-l1 §5 + the T2 join contract) that are not
--    uuids. The host key space widens to text; apply's deterministic
--    uuid5 ids and existing rows are untouched (a uuid's text form is a
--    legal text id). placements.host_id follows its foreign key.
--
-- 2) join_tokens lacks created_at: the admin-side `token list` orders
--    newest first and the list view shows creation time, so the column
--    lands now (NOT NULL DEFAULT now() backfills existing rows).

ALTER TABLE placements DROP CONSTRAINT placements_host_id_fkey;
ALTER TABLE hosts ALTER COLUMN id TYPE text;
ALTER TABLE placements ALTER COLUMN host_id TYPE text;
ALTER TABLE placements ADD CONSTRAINT placements_host_id_fkey FOREIGN KEY (host_id) REFERENCES hosts (id);

ALTER TABLE join_tokens ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
