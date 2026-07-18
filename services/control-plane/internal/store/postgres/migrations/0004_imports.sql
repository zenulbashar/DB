-- 0004_imports: migration/import jobs (MIGRATION_STRATEGY, API_SPECIFICATION).
-- The source connection string lives ONLY in secrets (envelope-encrypted).

CREATE TABLE imports (
    id               text PRIMARY KEY,
    project_id       text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    org_id           text NOT NULL,
    target_branch_id text REFERENCES branches(id),
    source_kind      text NOT NULL CHECK (source_kind IN ('neon','supabase','rds','azure','generic')),
    mode             text NOT NULL CHECK (mode IN ('dump_restore','logical_replication')),
    state            text NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending','preflight','schema_copy','initial_copy',
                                      'live_sync','cutover_ready','cut_over','verified',
                                      'failed','aborted')),
    source_secret_id text NOT NULL REFERENCES secrets(id),
    report           jsonb,
    checkpoints      jsonb,
    error            text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX imports_project_idx ON imports (project_id);
CREATE INDEX imports_org_idx ON imports (org_id);
-- Runner work queue: pending/running states only.
CREATE INDEX imports_active_idx ON imports (state)
    WHERE state NOT IN ('verified','failed','aborted');

ALTER TABLE imports ENABLE ROW LEVEL SECURITY;
ALTER TABLE imports FORCE  ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON imports
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');
