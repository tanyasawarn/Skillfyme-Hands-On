-- Revised lifecycle requirement §5: "restore last saved snapshot" on
-- resume. No real workspace/filesystem capture exists yet anywhere in
-- this codebase (the Go orchestrator's Snapshot/Restore RPCs are
-- Unimplemented stubs) -- this migration adds the data-model shape only
-- (columns + the SNAPSHOT_TAKEN event already declared in the taxonomy
-- now actually gets fired), so the API/DB surface is ready for real
-- snapshot capture as separate follow-up work. Until then,
-- snapshot_id/snapshot_taken_at are always null and reactivate() still
-- provisions a fresh environment, same as before this migration.

ALTER TABLE attempt.attempt
  ADD COLUMN IF NOT EXISTS snapshot_id text,
  ADD COLUMN IF NOT EXISTS snapshot_taken_at timestamptz;
