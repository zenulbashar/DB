# Roadmap — NimbusDB

**Status:** Draft v0.1 · Phase ordering is authoritative in [MASTER_IMPLEMENTATION_PLAN.md](MASTER_IMPLEMENTATION_PLAN.md) §5; this document details scope and acceptance criteria. No calendar dates — phases gate on their exit checklists, not on the calendar; sequencing assumes a single senior engineer + AI-assisted implementation, so phases are sized to be individually shippable.

---

## Phase 0 — Planning & architecture ✅ (this document set)

**Deliverable:** the 12 documents in `/docs/architecture`, committed.
**Acceptance:** internally consistent; open questions listed in DECISION_LOG; owner approval before Phase 1 code.

## Phase 1 — Foundations & control plane core

**Scope**
- Monorepo scaffold per MASTER plan §4; Go workspace; Next.js console shell; CI (GitHub Actions: lint, typecheck, unit tests, build; path-filtered).
- Local dev environment: `kind` cluster bootstrap script with CNPG + Cilium + Traefik installed; docker-compose fallback for pure-API work.
- `api/openapi.yaml` v1 skeleton (orgs, projects, api-keys, auth) + generated TS client.
- Control-plane API: health, authn (`ndb_` API keys + one-time bootstrap flow; console sessions arrive with the console in Phase 3 — ADR-013), org/user/member CRUD, API key issuance/revocation, project records CRUD, audit-log writes; control-plane Postgres with migrations; RLS + repository-layer scoping.
- ArgoCD app-of-apps skeleton; Terraform module skeleton for the `syd1` substrate (ADR-005 decides the substrate at this point).

**Acceptance**
- `make dev` yields a working local stack; CI green; an API key can create an org/project *record* (no data plane yet); audit rows written; docs updated.

## Phase 2 — Managed Postgres v1

**Scope**
- Reconciler: project/branch desired state → namespace, quotas, NetworkPolicies, CNPG `Cluster`, `Pooler`, TLS certs, `ScheduledBackup`.
- pg-gateway v1: TLS termination, SNI routing, startup-parameter fallback, route table watching, connection counters. No wake logic yet.
- Endpoints: `rw-direct` + `rw-pooled` live; connection-string surfacing in API.
- WAL archiving + nightly base backups to object storage; **nightly restore-verification job** (DATABASE_ARCHITECTURE §3).
- Role & database management API (create role/db, reset password → envelope-encrypted secrets).

**Acceptance**
- `POST /v1/projects` → connectable Postgres (both endpoints, TLS) in < 60 s on staging.
- Kill-the-primary chaos test: HA tier fails over < 30 s, no data loss (synchronous tier).
- PITR restore to a new branch verified end-to-end. Roster's and Prompt2Eat's schema dumps load cleanly (rehearsal only).
- pgbench baseline recorded (pooled + direct) for the performance-review gate.

## Phase 3 — Console v1 & developer surface

**Scope**
- Console auth: email magic-link sessions (+ the SECURITY_MODEL §3 session semantics; ADR-013 moved this here from Phase 1).
- Console: org/project dashboards, connection-string panel (copy variants for `pg`, Drizzle, Prisma, psql), role management UI, branch list, metrics dashboards (Prometheus-backed), audit-log viewer, API-key management, secrets/connection-string rotation flows.
- SQL editor (CodeMirror 6) with per-branch scoped execution through a control-plane proxy endpoint (short-lived credentials, statement timeout, result-size caps) — never direct browser→DB.
- Database import/export v1: `pg_dump` download / upload-and-restore into a branch (size-bounded; large imports arrive in Phase 5).
- Design system per DESIGN_SYSTEM_MAPPING.md (token layer + primitives).

**Acceptance**
- A new user can sign up, create a project, run SQL, see metrics, rotate a password — no CLI needed. Accessibility pass (keyboard nav, contrast) on core flows. Docs + CHANGELOG updated.

## Phase 4 — Elastic compute

**Scope**
- Scale-to-zero: suspend on idle, gateway wake-on-connect (hold + resume), per-plan `suspend_timeout`.
- Vertical autoscaling between CU bounds; zero-downtime resize on HA tier (replica-first switchover).
- Read replicas + `ro-pooled` endpoint.
- Branching/cloning v1 (snapshot + PITR modes) and instant restore via endpoint promotion.

**Acceptance**
- Wake p50 < 10 s / p95 < 25 s measured over 100 cycles on staging; zero dropped bytes on held connections.
- Branch-from-now on CoW storage < 30 s to connectable; restore-and-promote drill documented as a runbook and executed.
- Both customer-app rehearsal branches (copies of their schemas + representative data volumes) exercised under load with autoscaling active.

## Phase 5 — Migration engine & first-customer cutover

**Scope**
- Temporal deployment; import workflows: preflight (version/extension/collation checks, size estimate), schema migration, initial copy, **logical-replication live-sync mode**, sequence sync, cutover checklist, verification (row counts, checksums on sampled tables).
- Source adapters: Neon, Supabase, RDS, Azure, generic Postgres (see MIGRATION_STRATEGY.md §4 — mostly configuration differences over the same engine).
- Console "Import database" flow with progress + cutover runbook rendering.
- **Execute the two production migrations** (runbooks in MIGRATION_STRATEGY §6–7): Roster (env-swap only) and Prompt2Eat (driver-swap PR + env swap).

**Acceptance**
- Both apps in production on NimbusDB ≥ 14 days with error budgets intact; Neon projects decommissioned; rollback plans retired. This is the platform's proof gate.

## Phase 6 — Nimbus integration

**Scope**
- Nimbus-side `DatabaseProvider` (their repo, their driver idiom) + NimbusDB service-key kind + webhooks.
- Env-var injection contract (`DATABASE_URL`, `DATABASE_URL_DIRECT`) incl. rotation propagation.
- Console deploy actions: Deploy Compute / API / Worker / Cron / Frontend → Nimbus API (`site`/`agent` kinds today; worker/cron ride on `agent` until Nimbus adds first-class kinds).
- Attach/detach workload flows both directions; shared usage surfacing into Nimbus's plan/usage UI.

**Acceptance**
- From NimbusDB console: provision DB → deploy a sample API on Nimbus with injected connection string → detach cleanly. From Nimbus: "Add database" → project provisioned in NimbusDB. Contract documented in both repos.

## Phase 7 — Commercial readiness

**Scope**
- Metering pipeline (NATS → aggregation → ClickHouse), plans/quotas enforcement, Stripe billing, invoices.
- Admin portal (platform-operator console): tenant search, plan overrides, incident tooling, abuse controls.
- Query insights (pg_stat_statements → ClickHouse → console).
- SOC2-readiness: control mapping, access reviews, log retention policies, pen-test remediation (SECURITY_MODEL §8).
- External-tenant onboarding: signup hardening, email verification, abuse/rate limits, docs site.

**Acceptance:** first external tenant self-serves a migration from Neon/Supabase without operator involvement; invoices reconcile against metered usage within 1%.

## Phase 8 — Scale & serverless Gen 2

**Scope**
- Multi-region: region-scoped control planes, `region` in all placement APIs already; global console; cross-region replicas.
- Cells for large tenants; capacity management automation.
- **Gen 2 storage evaluation (ADR-004):** self-hosted Neon OSS storage engine (pageserver/safekeepers) behind the same API for serverless-tier branches — decision gate with a written evaluation: cold-start, branch-time, ops burden, cost/GB.
- Kick off first roadmap modules (§4) as separate phased plans.

---

## 4. Long-term module roadmap (design-for now, build later)

The platform primitives every module reuses: org/project tenancy, per-project namespaces, the
event bus, envelope-encrypted secrets, metering meters, and the console shell. Each module gets
its own architecture doc before code (docs-first rule).

| Module | Natural building blocks already in the platform |
|---|---|
| Object Storage | MinIO/S3 multi-tenant buckets + the existing per-project credential model |
| Queues | NATS JetStream tenant namespaces; or Postgres-backed queues per branch |
| Cron | Reuses the reconciler + a scheduler service; Nimbus cron workloads for compute |
| Serverless Compute / Functions | Nimbus's domain — integrate, don't duplicate (ADR-008) |
| Realtime | Logical decoding (wal2json) per branch → NATS → websocket edge |
| Vector Database | `pgvector` extension on existing branches (allowlisted already) + index advisories |
| Search | pg_trgm/FTS first; external engine only with proven demand |
| AI Gateway | Stateless proxy service; per-org keys/quotas via existing secrets + metering |
| Analytics | ClickHouse tenancy (shared cluster, per-org databases) |
| Email | Provider integration (Resend/SES) + per-org domains; both customer apps already use Resend |
| Secrets | Promote the internal envelope-encryption service to a tenant-facing product |
| Observability | Tenant-facing Grafana-style dashboards over the existing Prometheus/ClickHouse data |
