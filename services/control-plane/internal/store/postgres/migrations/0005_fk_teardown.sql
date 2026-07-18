-- 0005_fk_teardown: let a branch hard-delete succeed even when other rows
-- still reference it (audit: reconciler teardown wedged forever on a NO ACTION
-- FK violation once the CNPG resources were already gone).
--
-- branches.parent_id and imports.target_branch_id both referenced branches(id)
-- with the implicit NO ACTION, so FinishBranchTeardown's DELETE raised 23503
-- whenever a child branch or an import row still pointed at the branch. Switch
-- both to ON DELETE SET NULL: lineage/targets become dangling (correct — the
-- branch is gone) rather than blocking the delete.

ALTER TABLE branches DROP CONSTRAINT branches_parent_id_fkey;
ALTER TABLE branches
    ADD CONSTRAINT branches_parent_id_fkey
    FOREIGN KEY (parent_id) REFERENCES branches(id) ON DELETE SET NULL;

ALTER TABLE imports DROP CONSTRAINT imports_target_branch_id_fkey;
ALTER TABLE imports
    ADD CONSTRAINT imports_target_branch_id_fkey
    FOREIGN KEY (target_branch_id) REFERENCES branches(id) ON DELETE SET NULL;
