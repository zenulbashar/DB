# Changelog

All notable changes to this repository. Format loosely follows [Keep a Changelog](https://keepachangelog.com/); one entry per phase gate plus notable intermediate merges.

## [Phase 5 groundwork — pulled forward]

### Added (import-engine preflight, 2026-07-18)
- `services/import-engine`: source-database preflight (MIGRATION_STRATEGY §2 stage 1) —
  read-only catalog inspection producing the gating report: server version, database size,
  `wal_level`, replication-slot capacity, REPLICATION privilege, extensions vs the target
  allowlist, per-table PK/replica-identity audit, enums, sequences; mode recommendation
  (dump_restore < 10 GiB < logical_replication) and blocker/warning derivation with
  per-source remediation (Neon direct-host/autosuspend, Supabase platform-schema scope,
  RDS parameter-group + slot-storage, Azure server parameter, generic).
- `cmd/preflight` CLI printing the JSON report (exit 3 on blockers).
- Integration tests against real Postgres fixtures (enum + PK-less + serial tables);
  live CLI smoke verified against the local instance. CI job + Makefile added.

### Added (dump/restore + verification, 2026-07-18)
- `internal/dumprestore`: pg_dump custom-format → pg_restore (`--no-owner
  --no-privileges --exit-on-error`, optional parallel jobs, pinned binary dir).
- `internal/verify` (MIGRATION_STRATEGY §3): table-set parity, exact row counts,
  deterministic sampled content checksums (hash-ordered, physical-order-independent),
  sequence `last_value` ≥ source (duplicate-key guard), enum label parity.
- End-to-end integration: fixture DB (enums, FK pair, sequences, 2k rows) migrated and
  verified clean; tampering test proves checksum, row-count, and sequence regressions
  are each caught. Import-engine CI pinned to postgres:16 (client-binary major match).

## [Phase 2 — in progress]

### Added (2a: branch & endpoint resource model, 2026-07-17)
- Migration `0002_branches`: `branches` + `endpoints` tables with the same FORCE-RLS
  discipline as 0001; `projects.default_branch_id`.
- Project creation now atomically provisions the default branch `main` (role `production`,
  compute defaults 0.25–2 CU, 300 s suspend timeout) with `rw_direct` + `rw_pooled` endpoint
  records in `provisioning` state; endpoint hosts follow
  `ep-<ulid>.<region>.db.nimbus.app` (DATABASE_ARCHITECTURE §5).
- API: `GET/POST /projects/{prj}/branches`, `GET/PATCH/DELETE /branches/{br}`,
  `GET /branches/{br}/endpoints` with `branches:*`/`endpoints:read` scopes, CU/suspend/retention
  validation, default-branch delete protection (409; project deletion is the cascade path).
- OpenAPI: branch/endpoint paths + schemas; TS client regenerated.
- Tests: unit lifecycle/validation/scope suites; integration coverage for atomic
  default-branch provisioning, cross-org RLS on the new tables, and cascade semantics.

### Added (2b: pg-gateway v1, 2026-07-18)
- `services/pg-gateway`: Postgres wire-protocol TCP gateway (ADR-007) — SSLRequest/GSSENC/
  Cancel/Startup handshake handling, client TLS termination, **SNI routing**
  (`ep-<id>.<region>.db.nimbus.app` → endpoint), `options=endpoint%3D<id>` fallback with the
  routing token **stripped before backend forwarding** (backends reject unknown server args —
  caught by the e2e suite), per-endpoint connection caps, suspended-endpoint rejection
  (57P03; Phase 4 replaces with hold-and-wake), Postgres-native error responses, Prometheus
  metrics (`pggw_*`) + health endpoint, hot-reloading file route table that keeps the last
  good version on reload failure.
- E2E integration tests drive a real pgx client through the gateway to live Postgres:
  SNI routing (simple + extended protocol), options fallback, unknown/suspended endpoint
  rejection, connection-cap enforcement and release, plaintext rejection (TLS-only posture).
- CI: dedicated pg-gateway job (gofmt/vet/e2e vs postgres:17/build); Makefile targets.

### Added (2c: reconciler + CNPG provisioning, 2026-07-18)
- `cmd/reconciler` + `internal/reconciler`: desired-state convergence loop — per-project
  namespace with ResourceQuota and default-deny + allow-gateway NetworkPolicies
  (MULTI_TENANCY §2/§3), CNPG `Cluster` per branch (production role → 2 instances,
  guaranteed-QoS sizing from CU, `pg_stat_statements` preloaded, superuser access off),
  transaction-mode `Pooler` with `max_prepared_statements` for extended-protocol clients,
  readiness detection via CNPG `status.readyInstances` → branch/endpoints flip to `ready`,
  teardown (deleting branches → k8s objects removed, namespace removed with the last branch,
  rows purged), and gateway route-table ConfigMap publication (backend =
  `<cluster>-rw/-pooler/-ro.<ns>.svc:5432`).
- Privileged reconciler store methods (`ListReconcileWork`, `MarkBranchReady`,
  `FinishBranchTeardown`, `CountLiveBranches`, `ListRoutableEndpoints`) — platform-actor
  paths, never exposed via the API.
- Tests: fake-client suite (provision shape, readiness gating, idempotent re-runs, teardown,
  route ConfigMap contents) + store integration flow (work queue → ready → routable →
  teardown drained).

### Added (2d: envelope secrets + role/database API, 2026-07-18)
- `internal/secrets`: AES-256-GCM envelope encryption (per-secret DEK wrapped by versioned
  KEK; keyring from `NDB_KEKS`/`NDB_ACTIVE_KEK`, rotation-ready; KMS replaces the keyring
  without a blob-format change). URI-safe credential minting.
- Migration `0003`: `secrets`, `db_roles`, `databases` tables under FORCE-RLS.
- Project creation now seeds the default branch with `<name>_owner` role + database —
  password returned exactly once (`ProjectCreated` response shape).
- API: role CRUD + reset-password (password-once semantics), database CRUD
  (owner-role delete protection), and `GET /projects/{prj}/connection-uri` — masked by
  default, `?reveal=true` gated on `roles:write` and always audited. Reserved PG name
  guardrails (`postgres`, `pg_*`, CNPG-internal roles).
- Verified: secrets unit suite (roundtrip, tamper, wrong-key, rotation), handler suites,
  Postgres integration (seed flow, secret rotation, RLS cross-org zero-leak on new tables),
  plus a live end-to-end smoke: bootstrap → seeded project → masked URI → audited reveal
  decrypting the exact creation-time password.
- Test harness hardened: schema-level reset + catalog-derived truncation (no more stale
  table lists as migrations land).

### Added (2e: WAL archiving/backup + recovery specs, 2026-07-18)
- `BackupConfig` on the reconciler (S3-compatible destination, per-project/branch
  `destinationPath` isolation, credentials secret refs, gzip WAL/data compression);
  Cluster spec gains `barmanObjectStore` + `retentionPolicy` from branch retention —
  with a 7-day floor guard (a zero-valued record can never render "0d" retention).
- `ScheduledBackup` per branch: nightly base backup with deterministic hash-spread
  scheduling (01:00–04:59) to avoid object-store stampedes; created on provision,
  removed on teardown.
- `BuildRecoveryCluster`: PITR bootstrap shape (external origin + optional targetTime,
  backup section stripped so clones never archive into the source's WAL stream) —
  shared by the restore-verification job, instant restore, and Phase 4 branching.
- Reconciler binary refuses to run without a backup bucket outside dev (risk R-2).
- Tests: backup spec shape, schedule determinism (idempotency), nil-config omission,
  recovery cluster shape incl. latest-vs-targetTime.

### Pending in Phase 2
- Nightly restore-verification job execution + backup-credentials secret replication
  into project namespaces; reconciler applying managed roles/databases to live clusters;
  TLS cert issuance per endpoint; audit writes moved into mutation transactions;
  live-cluster validation on kind/staging (fake-client coverage only so far — this
  environment has no Docker daemon).

## [Phase 1] — 2026-07-17

### Added
- **Monorepo scaffold**: Makefile, docker-compose (Postgres 17 + non-superuser app role),
  kind bootstrap script (`tools/dev-up.sh`), editorconfig, gitignore.
- **OpenAPI 3.1 contract** (`api/openapi.yaml`) for the Phase 1 surface: bootstrap, orgs,
  members, API keys, project records, audit log. Redocly-clean.
- **Go control-plane API** (`services/control-plane`): chi router, RFC 9457 problem responses,
  request-id/logging/recover middleware, `ndb_` API-key auth (SHA-256 at rest, scoped,
  reveal-once), one-time bootstrap flow (ADR-013), org/member/key/project CRUD with audit
  writes, Idempotency-Key replay for POSTs, cursor pagination.
- **Postgres store** with embedded migrations, advisory-locked migrator, and **row-level
  security** on all org-scoped tables (FORCE RLS; `app.current_org` per transaction;
  append-only audit via absent UPDATE/DELETE policies) plus an in-memory store for unit tests.
- **RLS bypass guard**: the API refuses to start if its DB role is superuser/BYPASSRLS.
- **Console shell** (`console/`): Next.js 15 + Tailwind v4 with the DESIGN_SYSTEM_MAPPING token
  layer and seed primitives (Button, Card, Badge, StatusDot, ConnectionString with masking).
- **Generated TS client** (`packages/api-client`) via openapi-typescript; CI enforces spec/client sync.
- **CI** (`.github/workflows/ci.yml`): path-filtered jobs — Go (gofmt/vet/unit/integration vs
  postgres:17/build), console (typecheck/build), API contract (redocly lint + client-sync check).
- **Deploy skeletons**: Terraform substrate module structure, Kustomize layout, ArgoCD app-of-apps.

### Verified
- Unit + integration suites green locally (integration against real Postgres 16, non-superuser
  role); end-to-end HTTP flow exercised against a live binary: health → bootstrap (once-only,
  409 on repeat) → key-authed project create → idempotency replay → 401 unauthenticated →
  audit entries present. RLS cross-org leak test and audit immutability test pass.

### Changed (docs-first sync)
- API_SPECIFICATION: scope list gains `orgs:write`, `members:manage`.
- SECURITY_MODEL: control-plane DB role must be `NOSUPERUSER NOBYPASSRLS` (startup-enforced);
  audit writes are post-commit best-effort in Phase 1, in-transaction from Phase 2.

### Review notes (phase-gate lenses)
- *Principal Engineer*: store interface keeps handlers thin; slug-collision retry simplified
  after review; memory/postgres stores share semantics via the same test expectations.
- *Security Architect*: RLS + repository scoping double-net verified by tests; superuser bypass
  caught by integration suite and now startup-enforced; tokens hashed, reveal-once, constant-time
  bootstrap compare; cross-org probes return 404.
- *SRE*: graceful shutdown, health endpoint, JSON logs with request IDs; migrations
  advisory-locked for rolling deploys; audit write failure is logged, not user-facing.
- *Database Engineer*: append-only audit enforced at the policy layer; additive-only migration
  discipline documented; partial unique index frees slugs of deleted projects.
- *Performance*: CRUD-only phase — each org-scoped call costs one extra round trip
  (`set_config`) inside its transaction; acceptable now, pgbench baselines land with the Phase 2
  data path.

### Next
- Phase 2: reconciler + CNPG provisioning, pg-gateway v1, WAL archiving/PITR, restore-verification job.

## [Phase 0] — 2026-07-17

### Added
- Complete architecture documentation set under `docs/architecture/` (12 documents):
  master implementation plan, system architecture, database architecture, multi-tenancy,
  roadmap, API specification, security model, deployment architecture, design system mapping,
  risk register, migration strategy, decision log.
- Repository README with documentation index and working agreements.

### Analysis performed (inputs to the plan)
- `zenulbashar/hosting` (Nimbus): control-plane architecture, `DeploymentDriver` extension seam,
  auth/token model, env-var injection contract, design tokens — integration contract derived.
- `zenulbashar/roster-tool` (Roster): Drizzle + `pg`, pooled/direct dual-endpoint dependency
  (pg-boss worker), no extensions, DB sessions — zero-code migration runbook derived.
- `zenulbashar/order-tool` (Prompt2Eat): Neon WebSocket driver (swap required), interactive
  transactions, 28 enums, no extensions — one-PR migration runbook derived.
- Prompt2Eat design handoff bundle: adopted as token-layer/handoff format for the console.

### Decisions opened (awaiting owner)
- ADR-001 name, ADR-005 substrate, design export (Q3), billing processor (Q4), migration order (Q5)
  — see `docs/architecture/DECISION_LOG.md`.

### Next
- Phase 1 (foundations & control-plane core) begins after plan approval.
