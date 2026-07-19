-- 0009_branch_bootstrap: point-in-time branching target (ADR-016).
--
-- A non-root branch (parent_id set) is a data fork provisioned by CNPG recovery
-- from the parent's WAL archive. bootstrap_at is the optional recovery target:
-- NULL branches from the latest archived WAL ("now"); a timestamp branches from
-- that point in time. It is a one-time bootstrap parameter, immutable after
-- provisioning.

ALTER TABLE branches ADD COLUMN bootstrap_at timestamptz;
