-- 0011_restore_verification: restore-verification ledger (R-2 — "a backup you
-- haven't restored is a hope, not a backup"). One row per branch, holding the
-- latest verification attempt; the reconciler's verify loop drives it.

CREATE TABLE restore_verifications (
    branch_id   text PRIMARY KEY REFERENCES branches(id) ON DELETE CASCADE,
    org_id      text NOT NULL,
    status      text NOT NULL CHECK (status IN ('running','pass','fail')),
    message     text NOT NULL DEFAULT '',
    started_at  timestamptz NOT NULL DEFAULT now(),
    verified_at timestamptz
);
CREATE INDEX restore_verifications_status_idx ON restore_verifications (status);

-- Same RLS discipline as every tenant-scoped table (0001): org-scoped reads,
-- privileged platform actors (reconciler, admin surface) cross tenants.
ALTER TABLE restore_verifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE restore_verifications FORCE  ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON restore_verifications
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');
