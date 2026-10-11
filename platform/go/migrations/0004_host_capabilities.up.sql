-- topology slice 1.3 (#144): join-time machine facts (advisor-l1 §8).
-- Observed capabilities ride the join payload and land on the host
-- row: one jsonb column holding {hardware, network, collected_at} —
-- collected_at lives inside the document because it is meaningless
-- without the facts it stamps. NULL means "no facts collected" (an
-- old CLI's join), which the advisor reads as the missing-fact gap,
-- never a default. The re-join contract stays address/label-only, so
-- no write path touches this column after registration.

ALTER TABLE hosts ADD COLUMN capabilities jsonb NULL;
