-- 0010_vertical_resize: vertical autoscaling substrate (ROADMAP Phase 4).
--
-- current_cu is the branch's ACTUAL running compute size, between its
-- min_cu/max_cu bounds; the reconciler sizes the cluster's CPU/memory from it.
-- A resize is a transitional state (ready -> resizing -> ready) the reconciler
-- converges by re-applying the cluster at the new size — the same
-- zero-downtime, crash-safe shape as suspend/resume. NULL current_cu means
-- "not yet sized" and the reconciler falls back to min_cu.

ALTER TABLE branches ADD COLUMN current_cu double precision;

ALTER TABLE branches DROP CONSTRAINT IF EXISTS branches_state_check;
ALTER TABLE branches ADD CONSTRAINT branches_state_check
    CHECK (state IN ('provisioning','ready','suspending','suspended','resuming','resizing','error','deleting'));

ALTER TABLE endpoints DROP CONSTRAINT IF EXISTS endpoints_state_check;
ALTER TABLE endpoints ADD CONSTRAINT endpoints_state_check
    CHECK (state IN ('provisioning','ready','suspending','suspended','resuming','resizing','error','deleting'));
