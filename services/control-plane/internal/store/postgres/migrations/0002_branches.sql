-- 0002_branches: branch + endpoint desired-state records (DATABASE_ARCHITECTURE §1).
-- Phase 2a: records only; the reconciler (Phase 2c) drives state transitions.

CREATE TABLE branches (
    id                text PRIMARY KEY,
    project_id        text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    org_id            text NOT NULL,  -- denormalized for RLS + query locality
    parent_id         text REFERENCES branches(id),
    name              text NOT NULL,
    role              text NOT NULL DEFAULT 'development'
                      CHECK (role IN ('production','preview','development')),
    state             text NOT NULL DEFAULT 'provisioning'
                      CHECK (state IN ('provisioning','ready','suspended','error','deleting')),
    compute_min_cu    numeric(4,2) NOT NULL DEFAULT 0.25,
    compute_max_cu    numeric(4,2) NOT NULL DEFAULT 2,
    suspend_timeout_s int  NOT NULL DEFAULT 300,
    retention_days    int  NOT NULL DEFAULT 7,
    created_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz,
    CHECK (compute_min_cu > 0 AND compute_min_cu <= compute_max_cu)
);
CREATE UNIQUE INDEX branches_project_name_live_idx
    ON branches (project_id, name) WHERE state <> 'deleting';
CREATE INDEX branches_project_idx ON branches (project_id);
CREATE INDEX branches_org_idx ON branches (org_id);

CREATE TABLE endpoints (
    id         text PRIMARY KEY,
    branch_id  text NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
    org_id     text NOT NULL,  -- denormalized for RLS
    kind       text NOT NULL CHECK (kind IN ('rw_direct','rw_pooled','ro_pooled')),
    host       text NOT NULL UNIQUE,
    state      text NOT NULL DEFAULT 'provisioning'
               CHECK (state IN ('provisioning','ready','suspended','error','deleting')),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX endpoints_branch_kind_idx ON endpoints (branch_id, kind);
CREATE INDEX endpoints_org_idx ON endpoints (org_id);

ALTER TABLE projects ADD COLUMN default_branch_id text REFERENCES branches(id);

-- ---- Row-level security (same discipline as 0001) ---------------------------

ALTER TABLE branches  ENABLE ROW LEVEL SECURITY;
ALTER TABLE branches  FORCE  ROW LEVEL SECURITY;
ALTER TABLE endpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE endpoints FORCE  ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON branches
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');

CREATE POLICY org_isolation ON endpoints
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');
