-- 0003_roles_secrets: envelope-encrypted secret storage + per-branch Postgres
-- role and database records (DATABASE_ARCHITECTURE §8, SECURITY_MODEL §5).

CREATE TABLE secrets (
    id          text PRIMARY KEY,
    org_id      text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('db_password','webhook_secret','integration_token')),
    ciphertext  bytea NOT NULL,
    key_version int   NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    rotated_at  timestamptz
);
CREATE INDEX secrets_org_idx ON secrets (org_id);

CREATE TABLE db_roles (
    id         text PRIMARY KEY,
    branch_id  text NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
    org_id     text NOT NULL,
    name       text NOT NULL,
    secret_id  text NOT NULL REFERENCES secrets(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX db_roles_branch_name_idx ON db_roles (branch_id, name);
CREATE INDEX db_roles_org_idx ON db_roles (org_id);

CREATE TABLE databases (
    id            text PRIMARY KEY,
    branch_id     text NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
    org_id        text NOT NULL,
    name          text NOT NULL,
    owner_role_id text NOT NULL REFERENCES db_roles(id),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX databases_branch_name_idx ON databases (branch_id, name);
CREATE INDEX databases_org_idx ON databases (org_id);

ALTER TABLE secrets   ENABLE ROW LEVEL SECURITY;
ALTER TABLE secrets   FORCE  ROW LEVEL SECURITY;
ALTER TABLE db_roles  ENABLE ROW LEVEL SECURITY;
ALTER TABLE db_roles  FORCE  ROW LEVEL SECURITY;
ALTER TABLE databases ENABLE ROW LEVEL SECURITY;
ALTER TABLE databases FORCE  ROW LEVEL SECURITY;

CREATE POLICY org_isolation ON secrets
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');

CREATE POLICY org_isolation ON db_roles
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');

CREATE POLICY org_isolation ON databases
    USING (org_id = current_setting('app.current_org', true)
           OR current_setting('app.privileged', true) = 'on')
    WITH CHECK (org_id = current_setting('app.current_org', true)
                OR current_setting('app.privileged', true) = 'on');
