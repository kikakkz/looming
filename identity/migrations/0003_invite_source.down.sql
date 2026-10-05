-- Roll back the invite provenance columns (reverse order of 0003 up).
DROP INDEX IF EXISTS invite_tokens_bootstrap_source_uidx;
ALTER TABLE invite_tokens DROP COLUMN IF EXISTS email;
ALTER TABLE invite_tokens DROP COLUMN IF EXISTS source;
