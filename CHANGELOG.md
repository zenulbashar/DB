# Changelog

All notable changes to this repository. Format loosely follows [Keep a Changelog](https://keepachangelog.com/); one entry per phase gate plus notable intermediate merges.

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

### Pending in Phase 2
- 2c: reconciler + CNPG provisioning, WAL archiving/PITR, restore-verification job,
  audit writes moved into mutation transactions.

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
