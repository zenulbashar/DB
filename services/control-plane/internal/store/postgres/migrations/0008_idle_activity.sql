-- 0008_idle_activity: suspend-on-idle telemetry + grace baseline (ADR-015).
--
-- Gateways report per-branch active connection counts; the control plane
-- aggregates them across replicas and, when a branch is globally idle past its
-- suspend_timeout, flips it to suspending. last_active_at is the idle clock:
-- bumped on observed activity and seeded when a branch becomes ready/resumes so
-- a freshly-ready branch gets a full grace period before it can be suspended.

ALTER TABLE branches ADD COLUMN last_active_at timestamptz;

-- Ephemeral telemetry: one row per (branch, gateway). No FK — orphan rows for a
-- deleted branch are harmless (the sweep only considers ready branches) and a
-- report must never fail because a branch was concurrently torn down.
CREATE TABLE branch_activity (
    branch_id    text        NOT NULL,
    gateway_id   text        NOT NULL,
    active_conns integer     NOT NULL,
    reported_at  timestamptz NOT NULL,
    PRIMARY KEY (branch_id, gateway_id)
);

-- The sweep filters reports by recency (stale gateways age out of the sum).
CREATE INDEX branch_activity_reported_idx ON branch_activity (reported_at);
