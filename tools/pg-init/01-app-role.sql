-- Local-dev bootstrap: the control plane must connect as a NON-superuser role
-- so row-level security applies (the app hard-fails otherwise).
CREATE ROLE ndb_app LOGIN PASSWORD 'ndb_app' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
CREATE DATABASE nimbusdb_cp OWNER ndb_app;
