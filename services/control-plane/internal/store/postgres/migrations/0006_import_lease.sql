-- 0006_import_lease: give an import worker an exclusive, time-bounded claim on
-- an import for the whole duration of a stage — not just the microseconds of
-- the claim SELECT (audit: FOR UPDATE SKIP LOCKED released the lock at commit,
-- so a second replica re-claimed the same import and ran a concurrent
-- dump/restore into the target, corrupting it).
--
-- The lease is (claimed_by, claimed_at). A row is claimable when it is
-- actionable AND (unclaimed OR the lease has expired OR it is already ours).
-- A crashed worker's lease expires after the TTL and another worker resumes.

ALTER TABLE imports ADD COLUMN claimed_by text;
ALTER TABLE imports ADD COLUMN claimed_at timestamptz;

-- Supports the claim ORDER BY over the actionable set.
CREATE INDEX imports_claimable_idx ON imports (updated_at)
    WHERE state NOT IN ('cutover_ready', 'verified', 'failed', 'aborted');
