# Changelog

All notable changes to this repository. Format loosely follows [Keep a Changelog](https://keepachangelog.com/); one entry per phase gate plus notable intermediate merges.

## [Phase 4 — gateway wake API surface] — 2026-07-18

The control-plane half of gateway wake-on-connect (ADR-014 addendum): the privileged endpoint the
pg-gateway will call to wake a suspended branch, plus the routing data it needs. The gateway-side
hold/coalesce/poll logic is the next increment.

### Added
- **Internal wake endpoint** `POST /internal/branches/{br}/wake` — privileged and cross-tenant
  (the gateway serves every tenant, so it cannot use the org-scoped `POST /branches/{br}/resume`).
  It resolves the branch's org internally and performs the same idempotent `suspended → resuming`
  flip, so the human resume and the gateway wake converge through one transition. Authenticated by
  a shared `NDB_GATEWAY_TOKEN` bearer (constant-time compared, bootstrap-token shape); the whole
  `/internal` surface is **disabled when the token is unset**. Every call is audited
  (`branch.wake`, actor = system). 401 without the token, 404 for an unknown branch, 409 for a
  branch not in a wakeable state.
- **`WakeBranchByID`** store method (Postgres + memory) — resolves the org, then reuses the
  org-scoped `ResumeBranch` transition so the state-machine logic lives in one place.
- **`branch_id` in the route table** — the reconciler now emits `branch_id` per endpoint
  (`RoutableEndpoint` → route JSON → the gateway's `routes.Route`), so the gateway can map a
  connecting suspended endpoint to the branch it must wake.

### Security
- SECURITY_MODEL §3 documents the interim `NDB_GATEWAY_TOKEN` bearer as a scoped exception to
  "no shared static internal secrets" (single capability — flip to `resuming` by ID; cannot read
  secrets/provision/delete; internal-network only; pending mTLS).

### Tests
- Store round-trips for `WakeBranchByID` (suspended→resuming, idempotent, 404); server tests for
  the internal endpoint (token required, disabled when unset, 404/409 mapping); reconciler asserts
  `branch_id` in the emitted route JSON.

## [Phase 4 — scale-to-zero spine] — 2026-07-18

The control-plane half of serverless scale-to-zero: a branch/endpoint **compute state
machine** the reconciler converges into **CNPG hibernation**. This is the spine; the gateway
hold-and-wake and the idle-suspend detector are the follow-on increments that build on it.

### Decided
- **ADR-014 — wake/suspend are desired-state flips, not RPCs.** Resolves a pre-existing doc
  inconsistency (the SYSTEM_ARCHITECTURE §2 mermaid drew a forbidden direct `gateway → reconciler`
  edge). Suspend and wake are transitional branch states the reconciler converges; the gateway's
  on-connect wake (next increment) is a single **coalesced** authenticated POST to the control-plane
  API — a bounded, reviewed expansion of the gateway's "route, hold, count" scope (R-7), not DB
  access. Wake path = `gateway → API → control-plane DB → reconciler`, made explicit as the
  highest-availability tier (R-3).

### Added
- **Compute state machine.** New transitional states `suspending` (ready → suspending →
  suspended) and `resuming` (suspended → resuming → ready), shared by branches and endpoints and
  moved in lockstep. `domain.CanTransitionResource` validates the edges as defence-in-depth over
  the store's guarded SQL. Migration `0007` widens the `branches`/`endpoints` state CHECKs.
- **Store transitions.** `SuspendBranch`/`ResumeBranch` (org-scoped, **idempotent** — a repeat or a
  wake storm is a no-op 200, not a 409; illegal source state is 409, missing is 404) on both the
  Postgres and memory stores. Reconciler-side `MarkBranchSuspended`/`MarkBranchResumed` (privileged,
  guarded). `ListReconcileWork` and `ListRoutableEndpoints` now admit the transitional states, so a
  resuming branch stays in the route table (the gateway can hold/wake instead of 404-ing).
- **Reconciler hibernation.** `suspend`/`resume` convergence: `BuildCluster` toggles the CNPG
  `cnpg.io/hibernation` annotation (spec.instances is left at the role value — instances:0 is
  webhook-rejected), `BuildPooler` scales to zero when suspended, and `ensure()` now **merges**
  `metadata.annotations` (preserving operator-managed keys) so the toggle reaches an existing
  cluster. Suspend completes when CNPG reports no ready instances; resume reuses the existing
  ready gate.
- **API.** `POST /branches/{br}/suspend` and `/resume` (`branches:write`, audited), 409 on an
  illegal state transition. Spec-first: `api/openapi.yaml` + regenerated TS client.

### Notes
- The gateway needs **no change** this increment: `suspending`/`resuming` map down to `suspended`
  in the route table, so the gateway keeps cleanly rejecting a suspended endpoint until
  hold-and-wake replaces that rejection next.

### Tests
- Domain edge-legality table; memory + Postgres state-machine round-trips (lockstep, idempotency,
  cross-org 404, illegal-transition 409); reconciler suspend→hibernate→mark and
  resume→unhibernate→ready with the CNPG fake client; transitional→"suspended" route mapping; and
  HTTP wiring (route, scope, 409, 404).

## [Import-worker hardening audit] — 2026-07-18

A focused adversarial audit of the newly-live import worker and its migration runner
(each finding independently refute-verified before it counted) surfaced **11 confirmed
defects — 2 critical** — all fixed here with regression tests.

### Correctness / durability (critical)
- **Concurrent double-drive eliminated.** `ClaimActionableImport` now atomically *leases*
  an import (`claimed_by`/`claimed_at`, migration `0006_import_lease.sql`) in the same
  UPDATE that selects it, so the claim survives the transaction commit. The old
  `FOR UPDATE SKIP LOCKED`-only claim released its lock the instant the row was read, letting
  a second replica claim the same in-flight import and drive it in parallel (duplicate
  dump/restore, racing transitions). A crashed worker's lease expires after `leaseTTL`
  (default 30m, sized above the longest single stage) and another replica resumes it.
- **No more `live_sync → live_sync` self-transition.** The runner's stage loop
  (`advance` → `(progressed bool, error)`) now stays in the current state on a legal wait
  (initial copy not done, replication lag still non-zero) instead of re-issuing the current
  state as a transition. The state machine has no self-edges, so the old code failed every
  logical-replication migration on its first lag check.

### Durability
- **A failed logical migration can no longer leak a WAL-retaining slot.** `Step` now runs
  `cleanupOnFailure` before marking a job `failed`: it dials source and target independently
  and calls `logicalrepl.Abort` when both are reachable, or the new
  `logicalrepl.DropSourceObjects` (source-only slot + publication drop) when the target is
  down — the case that matters, since the orphaned slot pins WAL on the *source* forever.
- **Lag-poll connection leak fixed.** `runCutoverReady` dials only the source
  (`dialSource`); the previous code opened a target connection on every poll and never
  closed it.

### Security
- **Passwords kept out of `argv`.** `dumprestore` strips the password from the connection
  URL and passes it to `pg_dump`/`pg_restore` via `PGPASSWORD`, so it no longer appears in
  `ps`/`/proc/<pid>/cmdline` to any local user.
- **conninfo injection closed.** `urlToConnInfo` quotes/escapes every libpq keyword value,
  so a password (or any field) containing a space, quote, or backslash can neither break the
  conninfo nor smuggle in an extra keyword. A URL parse failure now returns a fixed,
  credential-free error instead of a `*url.Error` that embeds the raw URL.

### Correctness
- **Target owner role resolved by identity.** `ProductionTargetResolver` connects as the
  role that actually owns the target database (matched on `OwnerRoleID`), not an arbitrary
  `roles[0]`/`dbs[0]` pairing whose list order Postgres never guarantees.

### Tests
- New regression coverage: logical-replication end-to-end through the runner to `verified`
  with slot teardown asserted; failure-path slot cleanup; lease hand-off semantics
  (`TestImportClaimLease`); conninfo quoting + parse-error redaction; password splitting;
  and the runner's no-self-transition waits.

## [Import worker — migration engine goes live] — 2026-07-18

### Added
- **`cmd/import-worker` + `internal/importworker`**: the migration engine is now a runnable
  platform component, not just libraries. The worker adapts the shared import runner
  (`services/import-engine/runner`, now a public package) onto the control-plane store,
  claiming actionable imports, decrypting the source credential with the keyring, resolving
  the target connection, and persisting state transitions.
- **Secure-by-design credential handling**: the worker has direct database + keyring access,
  so decrypted source URLs never traverse the tenant HTTP API — the audit's credential-at-rest
  concern extended to dispatch. `store.ClaimActionableImport` uses `FOR UPDATE SKIP LOCKED`
  so worker replicas claim distinct jobs; `TransitionImportByID` is the privileged transition.
- **End-to-end integration test**: the worker drives a *real* dump_restore migration between
  two live databases to `verified` — claim from the control-plane DB → decrypt → preflight →
  dump/restore → operator cutover gate → full-table verify → data confirmed on the target.
  Plus a failure-path test (undecryptable source ⇒ job marked failed, queue not wedged).
- Cross-module wiring: control-plane now depends on `services/import-engine` (local replace);
  CI runs the worker integration path with a matching `pg_dump` client, serialized (`-p 1`)
  against the schema-recreating store suite.

## [Security & durability audit] — 2026-07-18

An 8-dimension adversarial audit (each finding independently verified before it counted)
over the whole implementation surfaced **19 confirmed defects**, all fixed here with
regression tests.

### Security
- **Idempotency cache no longer stores plaintext credentials.** Create responses carry
  one-time API tokens / DB passwords; the cached body in `idempotency_keys` is now
  envelope-encrypted with the keyring (leak-tested), and same-key POSTs are serialized per
  instance so two racing requests can't both create resources.
- **setval sequence-sync** passes the sequence name as a `regclass` parameter instead of
  interpolating a source-controlled identifier into SQL.

### Durability / correctness
- **Reconciler branch teardown no longer wedges forever.** `branches.parent_id` and
  `imports.target_branch_id` become `ON DELETE SET NULL` (migration 0005) so a referenced
  branch can still be deleted after its compute is gone; orphaned role secrets are cleaned
  up in the same transaction.
- **Migration parity verification is full-table by default** — a bounded sample could miss a
  single-row content corruption in any table larger than the cap. This is the cutover gate
  for real customer data, so the default now checksums every row.
- **Logical-replication cutover reordered to verify-then-cutover** (a failed verify was
  previously unrecoverable) and **teardown detaches the slot before dropping the
  subscription**, so an unreachable source no longer leaks a WAL-retaining replication slot.

### Data-plane isolation
- **Tenant NetworkPolicies corrected**: the ingress-only default-deny + gateway allow blocked
  CNPG streaming replication, the operator, and metrics scraping, and left egress wide open.
  Now default-deny covers **both** directions with explicit allow-lists (gateway,
  same-namespace replication, CNPG operator, monitoring; egress for DNS, replication,
  operator, and 443 for WAL archive).
- **Gateway per-endpoint connection cap is now populated** from the branch compute ceiling
  (it was always 0 = unlimited, making the cap dead code).

### Consistency / spec
- Postgres project-slug collision resolution now uses the same gap-filling loop as the memory
  store (they diverged: one 409'd where the other succeeded).
- Gateway `StripEndpointOption` handles the `-c endpoint=X` space-separated form without
  leaving a dangling `-c` the backend would reject.
- OpenAPI: `orgs:write` / `members:manage` added to the `Scope` enum; region constrained to
  `[syd1]` to match the handler; `/v1/healthz` alias so the documented API base resolves for
  generated clients.

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

### Added (import runner, 2026-07-18)
- `internal/runner`: transport-agnostic orchestration binding the import engine to the
  control-plane state machine. A `ControlPlane` interface (claim job → drive stage →
  transition) is implemented by an HTTP client in production and a fake in tests; the
  runner owns *how* each stage executes, the control plane owns *what state is legal next*
  (so a stale runner view cannot corrupt a job). One `Step` advances exactly one state;
  any stage error marks the job `failed` so a poisoned job never wedges the queue.
- Stage handlers per state for both modes: preflight (blocker→fail), dump/restore or
  schema-only+subscribe, initial-copy wait, lag-gated live-sync, sequence-sync+cutover,
  and parity-verified completion.
- Integration test drives a full **dump_restore import to `verified`** through the real
  transition rules and two real databases (400-row enum table lands intact), plus a
  failure-path test proving unreachable sources fail the job with a recorded message.
  This is the closest local proxy to the production Roster cutover.

### Added (imports resource + state machine, 2026-07-18)
- Migration `0004`: `imports` table under FORCE-RLS; source connection URLs stored ONLY
  as envelope-encrypted secrets (never returned by any read path — leak-tested).
- Import lifecycle state machine in the domain layer (`CanTransition`): dump_restore
  short-circuits `schema_copy → cutover_ready`, logical mode walks the full sync chain;
  no skipping, no reversing, `cut_over` may fail but not abort, terminal states final —
  enforced in the store under `FOR UPDATE` row locking.
- API: `GET/POST /projects/{prj}/imports`, `GET /imports/{imp}`, human-gated
  `POST /imports/{imp}/cutover`, `POST /imports/{imp}/abort`, and the runner-facing
  `PATCH /imports/{imp}/state` (report/checkpoint patches ride transitions atomically).
- OpenAPI + regenerated client; unit suites (transition matrix, full lifecycle over the
  API incl. 409s on illegal steps and credential-leak checks) and Postgres integration
  green.

### Added (logical-replication live-sync, 2026-07-18)
- `internal/logicalrepl` (MIGRATION_STRATEGY §2 stages 4–5): publication + **explicitly
  created replication slot** + subscription with `create_slot = false` — automatic slot
  creation deadlocks when publisher and subscriber share a cluster (found live by the
  integration suite), and the explicit slot makes Setup cleanly retryable (failed setup
  rolls back both slot and publication).
- Initial-copy tracking (`pg_subscription_rel`), source-side lag measurement (slot LSN
  delta → the API's `lag_bytes`), `WaitSynced`, sequence sync with optional margin,
  and `Cutover`/`Abort` teardown that also force-drops a leaked slot (the WAL-retention
  failure mode preflight warns RDS users about).
- `dumprestore` gains `SchemaOnly` (stage 3 of logical mode).
- Full migration rehearsal as an integration test: schema copy → subscribe → initial
  copy → live writes replicate → freeze → lag zero → sequence sync → cutover → slot gone
  → full parity verify → post-cutover independent writes (no duplicate-key risk).
  CI enables `wal_level=logical` on the service container so the rehearsal runs there too.

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
