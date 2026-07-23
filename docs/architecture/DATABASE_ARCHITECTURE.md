# Database Architecture — Zale DB

**Status:** Draft v0.1 · **Scope:** tenant data plane (managed Postgres) + control-plane schema.

---

## 1. Tenant database topology

Hierarchy (see MULTI_TENANCY.md for the tenancy model):

```
Organization → Project → Branch → Endpoints
```

- A **project** is the unit of provisioning and billing. Each project lives in its own
  Kubernetes namespace (`prj-<ulid>`).
- A **branch** is a full PostgreSQL cluster (CNPG `Cluster` CR) whose storage originates from
  its parent branch at a point in time. Every project has a default branch `main`
  (role: `production`). Branch roles: `production`, `preview`, `development` — this maps 1:1 to
  the environment model both customer apps and Nimbus already use.
- An **endpoint** is a stable connect address bound to one branch:
  - `rw-direct` — session-mode, straight to the primary (`cluster-rw` service).
  - `rw-pooled` — PgBouncer transaction mode.
  - `ro-pooled` — reads served by replicas (Phase 4).

**PostgreSQL versions:** 17 default for new projects; 16 supported (both first customers run
against stock PG 16 semantics today; no extension requirements found in either — verified from
their migration histories). Version upgrades via CNPG major-upgrade flow, offered per-branch.

### 1.1 CNPG cluster shape per branch

| Plan tier | Instances | Storage | Failover |
|---|---|---|---|
| Dev/preview branch | 1 | PVC (snapshot-backed if CoW available) | restart + WAL archive (RPO≈0, RTO minutes) |
| HA tier — production role, or any branch with a read endpoint (**builder-enforced**, ADR-019) | 2 | PVC + streaming replica, synchronous replication (`method: any, number: 1, dataDurability: preferred`) + replica-first controlled switchover; poolers run 2 replicas | automated, ~10–30 s; RPO ≈ 0 while a standby is healthy (degrades to async — never blocks writes — when it isn't) |
| Production (strict, future premium) | 3 | quorum sync replication (`required`) | automated, RPO 0 |

Compute sizes are t-shirt CUs (0.25–8 vCPU-equivalent) mapped to k8s requests/limits.
Autoscaling: vertical within bounds (Phase 4) driven by CNPG rolling restarts during low-traffic
windows or immediate for scale-up; scale-to-zero per §7.

---

## 2. Storage

- **PVCs on a CSI driver with snapshot support.** The concrete storage class is
  substrate-dependent (cloud: EBS/PD-class volumes; self-hosted: OpenEBS ZFS/Mayastor — chosen in
  Phase 1 bootstrap, ADR-005). Requirements: `VolumeSnapshot` support, expansion support,
  encryption at rest.
- **Object storage (S3-compatible)** holds: WAL archive (continuous), base backups (scheduled +
  pre-destructive-operation), branch-creation snapshots when CSI CoW isn't available, and
  import/export staging. Layout: `s3://ndb-<region>-wal/<project>/<branch>/...`,
  lifecycle-managed per retention plan.

## 3. Backups & PITR

- **Continuous archiving:** every branch archives WAL to object storage via CNPG's Barman Cloud
  plugin. Default retention: 7 days (dev), 30 days (production tier), plan-configurable.
- **Base backups:** nightly scheduled (`ScheduledBackup`), plus on-demand via API, plus
  automatically before destructive operations (branch delete, major upgrade, restore-in-place).
- **PITR:** restore any branch to any timestamp/LSN within retention → materializes as a **new
  branch** (never in-place by default; "instant restore" promotes that branch's endpoints over
  the old ones so the connection string keeps working — endpoint promotion, not data overwrite).
- **Verification (non-negotiable, R-2):** the reconciler runs a **restore-verification loop**
  (`NDB_VERIFY_INTERVAL`, e.g. `24h`; timeout `NDB_VERIFY_TIMEOUT`, default 30 m): for each ready
  branch whose archive hasn't been verified within the window it restores the WAL archive into an
  ephemeral recovery clone **beside the branch** (same namespace — that's where the archive
  credentials live; `-verify` name suffix, archiving stripped so the clone can't write into the
  source stream), records pass/fail in the `restore_verifications` ledger, and tears the clone
  down. Failures surface on the admin console as a data-durability page (R-2). A backup that
  hasn't been restored is assumed broken. Deeper checks (smoke queries, row-count parity against
  the live branch) layer onto the same loop later — clone-reaches-healthy already proves the
  archive restores.

## 4. Read replicas (Phase 4)

CNPG in-cluster replicas serve the `ro-pooled` endpoint (load-balanced across replicas,
`default_transaction_read_only=on` enforced). Replica count is a branch setting (0–5).
Cross-region read replicas are a Phase 8 concern (CNPG replica clusters via WAL shipping).

## 5. Connection pooling & endpoint semantics

This section is load-bearing — both first customers depend on it (Roster: `pg-boss` worker and
Drizzle migrations need session mode; Prompt2Eat: interactive `db.transaction()` calls).

| Endpoint | Mode | Semantics | Intended use |
|---|---|---|---|
| `rw-pooled` | PgBouncer **transaction** mode | Interactive multi-statement transactions **work** (server conn pinned for the tx). Session state (session-level prepared statements¹, advisory locks held across txs, `LISTEN/NOTIFY`, `SET` without `LOCAL`) **does not persist**. | Serverless/many-connection web apps (Vercel functions). |
| `rw-direct` | none (session) | Full Postgres semantics. Connection count bounded by `max_connections`. | Workers (pg-boss), migrations, admin tools, logical replication. |
| `ro-pooled` | PgBouncer transaction mode, replicas | Read-only. | Read scaling, analytics. |

¹ PgBouncer ≥ 1.21 supports protocol-level prepared-statement tracking in transaction mode
(`max_prepared_statements`); we enable it because node-postgres and Drizzle use extended-protocol
prepares.

Connection-string format (Neon-compatible shape, eases migration tooling and mental model):

```
postgresql://<role>:<password>@<endpoint-id>.<region>.db.zaleit.com.au/<database>?sslmode=require
```

TLS is required on all endpoints; SNI carries the endpoint ID for gateway routing. For clients
that cannot send SNI, the gateway falls back to an `options=endpoint%3D<id>` startup-parameter
route (same escape hatch Neon uses).

## 6. Branching, cloning, instant restore (Phase 4)

**Mechanism (Gen 1):**

1. **Branch from `now`:** CSI `VolumeSnapshot` of the parent's PVC → new PVC → new CNPG cluster
   with `pg_rewind`-safe divergence (new system identity via recovery). On CoW-capable storage
   this is O(seconds) and space-efficient; without CoW it degrades to snapshot-copy (documented
   per-substrate).
2. **Branch from point-in-time:** restore latest base backup before T + WAL replay to T
   (standard CNPG PITR bootstrap) → new cluster. O(minutes), proportional to WAL volume.
3. **Clone** = branch with `role=development` and no lineage-based retention coupling.
4. **Instant restore** = PITR branch + endpoint promotion (§3): the branch's stable endpoint IDs
   are re-pointed at the restored cluster; old cluster is retained for `restore_grace_period`
   (default 24 h) then reaped.

**Gen 2 (Phase 8 evaluation, ADR-004):** the open-source Neon storage engine
(pageserver/safekeeper, Apache-2.0) provides copy-on-write branching and PITR natively at the
page layer, plus sub-second compute cold starts. Adopting it replaces §2's PVC story for
serverless-tier branches. We evaluate rather than presume: it brings a distributed storage
system's operational burden. The public API contracts in API_SPECIFICATION.md are designed so
Gen 1 → Gen 2 is invisible to tenants.

## 7. Scale-to-zero & autoscaling (Phase 4)

Suspend and wake are **desired-state transitions on the branch** (`ready → suspending → suspended`
and `suspended → resuming → ready`) that the reconciler converges — not imperative RPCs (ADR-014).
The transitional state is the durable record, so both survive a reconciler restart. Endpoints move
in lockstep, and the route stays published (as `suspended`) throughout `suspending`/`resuming` so
the gateway can hold and wake a connecting client.

- **Suspend:** every gateway reports its per-branch active-connection counts to the control plane
  (`POST /internal/gateway-activity`); the **control plane** aggregates them across all replicas and
  runs the idle decision — never a single gateway, which sees only its own connections (ADR-015). A
  ready branch with `suspend_timeout_s > 0` is flipped to `suspending` only when the *globally*
  summed active count is zero AND it has been idle for `suspend_timeout` (default 300 s;
  `0` disables autosuspend, the paid-plan opt-out). The flip is **fail-safe** — it never fires when
  no gateway is currently reporting, so reporting downtime cannot mass-suspend the fleet. The
  reconciler then applies **CNPG hibernation** (`cnpg.io/hibernation` annotation: clean shutdown,
  PVCs kept), **scales the pooler to 0**, and marks the branch `suspended`. Suspended branches bill
  storage only. (The same `suspending` flip is reused by the billing path — MULTI_TENANCY §5 — not
  just idle detection.)
- **Wake:** the gateway holds the incoming connection and calls the control-plane **resume** action
  (`POST /branches/{br}/resume`), a coalesced, idempotent flip to `resuming`; the reconciler
  un-hibernates the cluster **and scales the pooler back up**, then marks the branch `ready` once
  CNPG reports ready. Targets: p50 < 10 s, p95 < 25 s (Gen 1 — set client `connect_timeout ≥ 30s`;
  documented). WAL archiving and scheduled backups pause while suspended (no changes to archive).
- **Compute autoscaling:** the branch carries a `current_cu` (its running size) which the
  reconciler applies to the cluster's CPU/memory, autoscaled between its `min_cu`/`max_cu` bounds.
  A resize is a transitional state — `ready → resizing → ready` — the reconciler converges by
  re-applying the cluster at the new size (in-place resource resize where the k8s version supports
  it, else rolling restart via replica-first switchover to keep it zero-downtime on HA tiers);
  routing is untouched throughout. `POST /branches/{br}/resize {cu}` sets the size manually and is
  the same action a metrics-driven autoscaler drives (the CPU/memory signal for the auto-decision
  arrives with the Phase 7 metrics pipeline).

## 8. Tenant database security defaults

Per-branch: TLS-only endpoints; one **owner role** (`<project>_owner`, NOSUPERUSER, no
`pg_read_server_files` etc.), additional roles manageable via API/console (role management UI);
`pg_stat_statements` enabled (insights); extension allowlist (curated, superuser-installed on
request: `pgcrypto`, `uuid-ossp`, `postgis`, `pgvector`, `pg_trgm`, …). No superuser access for
tenants — identical posture to Neon/RDS. Full model in SECURITY_MODEL.md.

## 9. Metrics & query insights

- **Per-branch metrics** (console dashboards, Phase 3): connections, TPS, cache hit ratio, IO,
  storage, replication lag, compute utilisation — from CNPG's exporter + gateway counters via
  Prometheus.
- **Query insights** (Phase 7): scheduled sampling of `pg_stat_statements` per branch →
  normalized query shapes (no parameter values — tenant data never leaves the branch) →
  ClickHouse → console "Query Insights" (top by total/mean time, calls, rows).

---

## 10. Control-plane database schema (v1 sketch)

PostgreSQL 17, single logical DB `nimbusdb_cp`, Go migrations (goose/atlas — final pick in
Phase 1). Multi-tenancy inside the control plane is row-scoped by `org_id` with every query
passing through a tenancy-checked repository layer (same predicate discipline Nimbus's
`PROJECT_ACCESS` uses today, plus RLS as defence-in-depth — MULTI_TENANCY §4).

Core tables (abridged; authoritative DDL will live in `/services/control-plane/migrations`):

```
orgs(id, name, slug, plan, created_at, …)
users(id, email, name, created_at, …)                  -- console identities
org_members(org_id, user_id, role)                      -- owner|admin|member|viewer
api_keys(id, org_id, name, hash, scopes[], last_used_at, expires_at, revoked_at)
projects(id, org_id, name, slug, region, pg_version, default_branch_id, nimbus_link jsonb, …)
branches(id, project_id, parent_id, name, role, state,  -- provisioning|ready|suspended|error|deleting
         source_lsn, source_timestamp, retention_days, compute_min_cu, compute_max_cu,
         suspend_timeout_s, created_at, deleted_at)
endpoints(id, branch_id, kind,                          -- rw_direct|rw_pooled|ro_pooled
          host, state, settings jsonb)
db_roles(id, branch_id, name, secret_id, created_by, …) -- tenant PG roles we manage
databases(id, branch_id, name, owner_role_id)
secrets(id, org_id, kind, ciphertext, key_version, created_at, rotated_at)  -- envelope-encrypted
backups(id, branch_id, kind, location, started_at, finished_at, size_bytes, state, verified_at)
restores(id, branch_id, target_branch_id, target_time, state, …)
imports(id, project_id, source_kind,                    -- neon|supabase|rds|azure|generic
        mode,                                           -- dump_restore|logical_replication
        state, workflow_id, checkpoints jsonb, …)
usage_events(rollup tables: hourly per org/project/branch/meter)
audit_log(id, org_id, actor_type, actor_id, action, target, ip, at, details jsonb)  -- append-only
webhooks(id, org_id, url, secret_id, events[], …)
desired_state / observed_state                           -- reconciler bookkeeping per resource
```

Conventions: ULID primary keys (sortable, no coordination); `timestamptz` everywhere;
soft-delete via `deleted_at` for tenant-visible resources; append-only `audit_log` (no UPDATE
grant, enforced by role); all secret material only in `secrets` (AES-256-GCM envelope,
SECURITY_MODEL §5).

## 11. Explicit non-goals (Gen 1)

- No shared-Postgres multi-tenancy for tenant data (every branch is its own cluster — isolation
  beats density at this stage; density levers are small CUs + scale-to-zero).
- No proprietary wire protocol, no HTTP/WebSocket SQL driver (Gen 2 candidate alongside the
  storage-engine evaluation; both customer apps are moving *off* the Neon serverless driver to
  plain `pg`, which our endpoints serve natively).
- No cross-branch queries, no logical decoding fan-out product (roadmap: Realtime module).
