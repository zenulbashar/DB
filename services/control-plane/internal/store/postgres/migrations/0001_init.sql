-- 0001_init: Phase 1 control-plane schema (DATABASE_ARCHITECTURE §10).
-- RLS discipline (MULTI_TENANCY §2): org-scoped tables carry FORCE ROW LEVEL
-- SECURITY with policies keyed on app.current_org; privileged paths (bootstrap,
-- key lookup) set app.privileged='on' inside a narrowly scoped transaction.

CREATE TABLE users (
    id          text PRIMARY KEY,
    email       text NOT NULL,
    name        text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_lower_idx ON users (lower(email));

CREATE TABLE orgs (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    slug        text NOT NULL UNIQUE,
    plan        text NOT NULL DEFAULT 'free',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE org_members (
    org_id      text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id     text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role        text NOT NULL CHECK (role IN ('owner','admin','member','viewer')),
    added_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

CREATE TABLE api_keys (
    id           text PRIMARY KEY,
    org_id       text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name         text NOT NULL,
    key_hash     text NOT NULL UNIQUE,
    prefix       text NOT NULL,
    scopes       text[] NOT NULL,
    created_by   text REFERENCES users(id) ON DELETE SET NULL,
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_org_idx ON api_keys (org_id);

CREATE TABLE projects (
    id          text PRIMARY KEY,
    org_id      text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name        text NOT NULL,
    slug        text NOT NULL,
    region      text NOT NULL DEFAULT 'syd1',
    pg_version  int  NOT NULL DEFAULT 17 CHECK (pg_version IN (16, 17)),
    state       text NOT NULL DEFAULT 'pending'
                CHECK (state IN ('pending','provisioning','ready','error','deleting')),
    nimbus_link jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz
);
-- Slug is unique among live projects per org; deleting frees the slug.
CREATE UNIQUE INDEX projects_org_slug_live_idx
    ON projects (org_id, slug) WHERE state <> 'deleting';
CREATE INDEX projects_org_idx ON projects (org_id);

CREATE TABLE audit_log (
    id          text PRIMARY KEY,
    org_id      text NOT NULL,
    actor_type  text NOT NULL CHECK (actor_type IN ('api_key','user','system')),
    actor_id    text NOT NULL,
    action      text NOT NULL,
    target_type text NOT NULL,
    target_id   text NOT NULL,
    ip          text,
    at          timestamptz NOT NULL DEFAULT now(),
    details     jsonb
);
CREATE INDEX audit_log_org_at_idx ON audit_log (org_id, id DESC);

CREATE TABLE idempotency_keys (
    org_id      text NOT NULL,
    route       text NOT NULL,
    idem_key    text NOT NULL,
    status      int  NOT NULL,
    body        bytea NOT NULL,
    expires_at  timestamptz NOT NULL,
    PRIMARY KEY (org_id, route, idem_key)
);

-- ---- Row-level security -----------------------------------------------------

ALTER TABLE orgs             ENABLE ROW LEVEL SECURITY;
ALTER TABLE orgs             FORCE  ROW LEVEL SECURITY;
ALTER TABLE org_members      ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_members      FORCE  ROW LEVEL SECURITY;
ALTER TABLE api_keys         ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys         FORCE  ROW LEVEL SECURITY;
ALTER TABLE projects         ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects         FORCE  ROW LEVEL SECURITY;
ALTER TABLE audit_log        ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log        FORCE  ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE  ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON orgs
    USING (id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (current_setting('app.privileged', true) = 'on'
                OR id = current_setting('app.current_org', true));

CREATE POLICY org_isolation ON org_members
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');

CREATE POLICY org_isolation ON api_keys
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');

CREATE POLICY org_isolation ON projects
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');

-- audit_log: INSERT allowed in org context; SELECT org-scoped; no UPDATE/DELETE
-- policies exist, so rows are immutable even for the app role (append-only,
-- SECURITY_MODEL §6).
CREATE POLICY audit_read ON audit_log FOR SELECT
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on');
CREATE POLICY audit_insert ON audit_log FOR INSERT
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');

CREATE POLICY org_isolation ON idempotency_keys
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');
