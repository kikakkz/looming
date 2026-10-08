-- Reverse of 0003. The explicit USING casts fail by design when rows
-- carry non-uuid ids (joined hosts' host-<uuid8>) — down migrations
-- exist for dev/test resets where only uuid-shaped ids exist.

ALTER TABLE placements DROP CONSTRAINT placements_host_id_fkey;
ALTER TABLE placements ALTER COLUMN host_id TYPE uuid USING host_id::uuid;
ALTER TABLE hosts ALTER COLUMN id TYPE uuid USING id::uuid;
ALTER TABLE placements ADD CONSTRAINT placements_host_id_fkey FOREIGN KEY (host_id) REFERENCES hosts (id);

ALTER TABLE join_tokens DROP COLUMN created_at;
