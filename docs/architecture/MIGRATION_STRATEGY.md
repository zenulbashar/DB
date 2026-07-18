# Migration Strategy — NimbusDB

**Status:** Draft v0.1 · Engine ships Phase 5; §6–7 are the concrete runbooks for the first two production migrations, grounded in analysis of both codebases (2026-07-17).

---

## 1. Product shape

"Import database" is a first-class platform feature (API: `POST /projects/{prj}/imports`,
console wizard), not a one-off script. Two modes:

| Mode | Downtime | Fit |
|---|---|---|
| **dump_restore** | Write-freeze for the dump+restore window | Small DBs (≲ 10 GB), dev/preview imports, sources without logical replication |
| **logical_replication** | Seconds-to-minutes at cutover | Production migrations; requires source `wal_level=logical` and replication privileges (Neon, Supabase, RDS, Azure Flexible Server all support this) |

Both run as Temporal workflows (durable, resumable, human-approvable cutover step) executed by
`/services/import-engine` workers; state surfaces via `imports.state`
(`preflight → schema_copy → initial_copy → live_sync → cutover_ready → cut_over → verified`).

## 2. Pipeline stages (logical_replication mode)

1. **Preflight** — connect with provided credentials (stored envelope-encrypted); collect: PG
   version, extensions in use, collation/ICU versions, database size, table list (with/without
   PKs — tables lacking PK/replica identity get `REPLICA IDENTITY FULL` guidance), sequences,
   enums, publications capacity, logical-replication privileges. Produces a written report with
   blockers/warnings (console-rendered).
2. **Target provisioning** — branch on the target project sized from the estimate; extensions
   pre-created from the allowlist; roles mapped (source superuser-ish roles flattened to
   `<project>_owner`; app roles recreated).
3. **Schema copy** — `pg_dump --schema-only` (source binaries version-matched) → apply; enums,
   defaults, expression indexes included; validation pass compares `pg_catalog` inventories.
4. **Initial copy + live sync** — `CREATE PUBLICATION` (source) / `CREATE SUBSCRIPTION`
   (target, `copy_data = true`); monitor `pg_stat_subscription` lag → `live_sync` with
   `lag_bytes` surfaced in API/console.
5. **Cutover (human-gated)** — checklist executed by the workflow: freeze writes (app-specific:
   maintenance mode / pause crons/workers) → wait lag=0 → **sequence sync** (logical replication
   does not carry sequences: `setval` from source values +Δ) → verification (row counts per
   table; checksums on sampled tables via `md5(array_agg(...))` batches) → drop subscription →
   flip application connection strings → smoke checks → unfreeze.
6. **Verified / rollback** — source is kept read-available until the tenant marks `verified`
   (or auto after N days). Rollback before cutover = abort (target discarded). Rollback after
   cutover within the grace window = reverse cutover runbook (source was kept frozen, re-point
   connection strings back; any writes made on target are lost — hence smoke checks before
   unfreeze).

`dump_restore` mode is stages 1–3 plus `pg_dump | pg_restore -j N` under a write freeze, then 5's
verification subset.

### 2.1 Worker orchestration semantics

The stages above are driven by the import worker (`cmd/import-worker`) against the control-plane
state machine, which owns *what state is legal next* (illegal transitions are rejected, so a
worker with a stale view cannot corrupt a job). The worker owns *how* each stage runs. Operational
guarantees:

- **Lease-based single-flight.** A worker *leases* the oldest actionable import (`claimed_by` +
  `claimed_at`, stamped atomically with the selecting `UPDATE`), and the lease outlives the
  claiming transaction. A second replica polling mid-stage sees a live foreign lease and skips the
  job — so two replicas never drive the same in-flight migration in parallel. A crashed worker's
  lease expires after `leaseTTL` (default 30 min, sized above the longest single stage) and another
  replica resumes the import from its persisted state.
- **Legal waits are not transitions.** The state machine has no self-edges. When a stage is
  legitimately waiting (initial copy not finished, replication lag not yet zero), the worker holds
  the current state and re-checks on the next poll — it does **not** re-issue the current state as a
  transition (which would be rejected and fail the migration).
- **Failure tears the link down.** Any stage error transitions the job to `failed`; for
  `logical_replication` jobs the worker first drops the replication objects — dialing source and
  target independently so it can still drop the WAL-retaining slot on the source even when the
  target is unreachable, the case where an orphaned slot would otherwise pin WAL forever.
- **Cutover is human-gated.** The worker never advances `cutover_ready`; an operator (or a
  policy-driven auto-cutover) makes that call, matching stage 5's freeze-and-flip checklist.

## 3. Verification (non-optional)

- Row-count parity per table; sampled checksum parity; sequence values ≥ source; enum/extension
  inventory parity; `ANALYZE` on target post-copy.
- Application-level: each runbook defines smoke queries (login round-trip, latest-order read,
  webhook write) executed before unfreeze.

## 4. Source adapters

The engine is one implementation; adapters encode source-specific preflight rules and connection
guidance:

| Source | Specifics |
|---|---|
| **Neon** | Use the **direct (unpooled)** host for replication (pooled endpoint can't hold replication slots). Roles lack superuser (fine — publication needs table ownership or `pg_create_publication`... preflight verifies). Watch for scale-to-zero suspending the source mid-sync: preflight instructs disabling autosuspend during migration. |
| **Supabase** | Postgres carries platform schemas (`auth`, `storage`, `realtime`, …): default import scope = app schemas only (`public` + user schemas), with an explicit option to bring `auth` data (maps to a plain schema on target; Supabase Auth features do not transfer — flagged in the preflight report). Uses session pooler or direct host for replication. |
| **AWS RDS / Aurora** | `rds.logical_replication=1` parameter-group change (+ reboot) required — preflight detects and instructs; `rds_replication` grant; long-lived slots watched (storage growth warning). |
| **Azure Database for PostgreSQL (Flexible)** | `wal_level=logical` server parameter + `REPLICATION` permission via `az` role grant; preflight verifies `azure.replication_support`. |
| **Railway / self-hosted / generic** | Direct superuser-ish access usually available; standard path. Version skew rule everywhere: dump binaries match source major; logical replication requires source ≥ 10; target may be a higher major (16→17 supported). |

## 5. Shared cutover principles for the first customers

Both apps (facts from repo analysis):
- Drizzle ORM, **additive-only CI migrations** to prod via a GitHub secret (`PROD_DATABASE_URL`) —
  so: target must be at the same Drizzle migration head before cutover, and **the GitHub secret
  flip is part of each runbook** (else post-cutover CI would migrate the dead Neon DB).
- Auth.js **database sessions** — carrying the `session` tables over (which logical replication
  does) preserves logins; a dump/restore cutover would too, but a "fresh schema" cutover would
  log every user out. Plan: carry sessions.
- Vercel `syd1` + Neon `ap-southeast-2` today → NimbusDB `syd1` keeps latency flat.
- Both currently ship `?sslmode=require` — endpoint parity, no app change.

## 6. Runbook: Roster (`zenulbashar/roster-tool`)

**Code changes required: none.** Driver is plain `pg`; swap env vars only.

| Item | Value |
|---|---|
| Topology | Vercel web (pooled URL) + Railway `pg-boss` worker (direct URL) + CI migrations (direct URL) |
| Target mapping | web → `rw-pooled`; worker → `rw-direct` (pg-boss needs session mode); CI `PROD_DATABASE_URL` → `rw-direct` |
| Size notes | Clock photos are `bytea` in `clock_photo` — initial copy is the bulk of transfer; estimate from preflight, budget copy time accordingly |
| pg-boss (`pgboss` schema) | **Not** in Drizzle migrations; created by worker at runtime. Decision: let the worker recreate it fresh on the target (accept loss of queued/scheduled-in-flight jobs) **or** include `pgboss` schema in replication if job continuity matters. Default: recreate fresh; drain the queue pre-cutover (stop enqueue paths during freeze, let worker finish jobs, stop worker). |
| Extensions | None (verified) — `gen_random_uuid()` is core |

**Cutover sequence:** enable maintenance banner → stop Railway worker (drains pg-boss) → pause
Vercel deploys → lag 0 → sequence sync → verify (incl. smoke: owner login via existing session,
roster read, availability write) → flip Vercel `DATABASE_URL` (pooled), Railway `DATABASE_URL`
(direct), GitHub `PROD_DATABASE_URL` (direct) → redeploy/restart → worker recreates `pgboss` →
smoke: enqueue+process a no-op job, magic-link email round-trip → unfreeze. Keep Neon read-only
7 days, then decommission.

## 7. Runbook: Prompt2Eat (`zenulbashar/order-tool`)

**Code change required: one PR (prepared before cutover, deployable independently):**
- `lib/db/index.ts`: `@neondatabase/serverless` + `drizzle-orm/neon-serverless` (WebSocket Pool +
  `ws`) → **`pg` + `drizzle-orm/node-postgres`** (identical shape Roster already uses; keeps
  `globalThis` caching).
- Remove `ws` dep and `serverExternalPackages: ["ws"]` → `["pg"]` in `next.config.ts`.
- No query changes: interactive `db.transaction()` works on PgBouncer transaction mode (server
  connection pinned per transaction) — gift-card/stock flows verified in staging rehearsal;
  extended-protocol prepares covered by `max_prepared_statements` (DATABASE_ARCHITECTURE §5).
- This PR can ship **against Neon first** (plain `pg` works fine on Neon) — decoupling driver
  risk from cutover risk. Recommended: ship it 1 week early.

| Item | Value |
|---|---|
| Topology | Vercel web (pooled) + Vercel Cron 03:00 (`/api/jobs/integrations`) + Stripe webhooks + CI migrations (direct) |
| Target mapping | web → `rw-pooled`; CI `PROD_DATABASE_URL` → `rw-direct` |
| Schema notes | ~28 `pgEnum`s + `lower(email)` unique indexes — carried by schema copy; verified in inventory parity check. App-generated UUIDs → no DB extension needs; sequences still synced (`venue_order_sequences` and any serial counters) |
| External state | R2 (images), Upstash (rate limits), Stripe — all unaffected; only `DATABASE_URL` moves |
| Write sources to freeze | Diner checkout (maintenance mode), Vercel Cron (disable), Stripe webhooks (Stripe retries for days — safe to let them 503 during the freeze window and replay after) |

**Cutover sequence:** driver-swap PR live ≥ 1 week (on Neon) → maintenance mode on storefronts →
disable cron → lag 0 → sequence sync → verify (smoke: owner session read, menu read, order
insert on a test venue, gift-card transaction) → flip Vercel `DATABASE_URL` + GitHub
`PROD_DATABASE_URL` → redeploy → re-enable cron → confirm Stripe webhook replay lands → unfreeze.
Keep Neon read-only 7 days, then decommission.

**Ordering:** Roster migrates first (no code change, lower write criticality — no live payments),
soaks ≥ 1 week, then Prompt2Eat. Both rehearsed end-to-end on staging branches with production
schema + representative data volume before the real windows (Phase 5 acceptance).

## 8. Post-migration

- Neon org closure after both grace windows; final logical dumps archived to platform object
  storage (encrypted) for 90 days.
- Both apps' `.env.example`/README connection docs updated to NimbusDB shapes (PRs in their repos).
- Lessons-learned written into the adapter preflight rules (the whole point of eating our own
  migration path before external tenants use it).
