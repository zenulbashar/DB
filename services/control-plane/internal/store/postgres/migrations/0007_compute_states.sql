-- 0007_compute_states: admit the transitional scale-to-zero states
-- (suspending, resuming) on branches and endpoints (ADR-014, Phase 4).
--
-- The reconciler only acts on transitional states, so suspend/wake are modelled
-- as ready → suspending → suspended and suspended → resuming → ready. The
-- inline CHECK from 0002 (auto-named <table>_state_check) only allowed the five
-- original values; widen both. Idempotent: drop-if-exists then re-add.

ALTER TABLE branches DROP CONSTRAINT IF EXISTS branches_state_check;
ALTER TABLE branches ADD CONSTRAINT branches_state_check
    CHECK (state IN ('provisioning','ready','suspending','suspended','resuming','error','deleting'));

ALTER TABLE endpoints DROP CONSTRAINT IF EXISTS endpoints_state_check;
ALTER TABLE endpoints ADD CONSTRAINT endpoints_state_check
    CHECK (state IN ('provisioning','ready','suspending','suspended','resuming','error','deleting'));
