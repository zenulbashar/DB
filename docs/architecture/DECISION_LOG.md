# Decision Log — NimbusDB

ADR format: context → decision → alternatives → consequences. Status: `proposed` (awaiting owner sign-off), `accepted`, `superseded`.
New ADRs are appended; superseding requires a link both ways.

---

> **2026-07-17 — Owner sign-off received.** All Phase-0 ADRs below moved `proposed → accepted`
> per owner review; open questions answered (see table at bottom). Phase 1 is authorized.

## ADR-001 · Product working name: "NimbusDB" — `accepted`
**Context:** The hosting platform brands as **Nimbus** (`nimbus.app`, `nbt_` tokens); company domain `zaleit.com.au`; the mission names no product name.
**Decision:** Use **NimbusDB** as the working name and `*.db.nimbus.app` as the endpoint namespace, keeping the platform family coherent.
**Alternatives:** standalone brand (marketing freedom, but fractures the "one platform" story); Zaleit-branded (company brand vs product brand unresolved).
**Consequences:** rename is cheap by policy — no identifier in code embeds the name except DNS + token prefix (`ndb_`), both centralized. **Owner (2026-07-17): "use it for now" — accepted as provisional; revisit before external GA (Phase 7).**

## ADR-002 · Go for control plane, gateway, and import engine — `accepted`
**Context:** Need a language for API + Kubernetes reconcilers + a wire-level TCP proxy. Ecosystem reality: controller-runtime/client-go, CNPG, ArgoCD, NATS, Temporal SDKs are Go-first. Existing estate is TypeScript (Nimbus, both apps) but has no k8s controller story.
**Decision:** Go ≥ 1.24 for all backend services; TypeScript stays for the console + generated API client.
**Alternatives:** Rust (stronger for a high-performance data path; slower CRUD/controller velocity; kept open for Gen-2 components), TypeScript everywhere (poor fit for reconcilers/proxy), mixed Rust-proxy+Go-API now (premature — gateway Gen 1 is routing/holding, not parsing-heavy).
**Consequences:** two-language monorepo; OpenAPI-generated TS client bridges the seam (spec-first rule).

## ADR-003 · Kubernetes + CloudNativePG as the data-plane substrate — `accepted`
**Context:** Need HA Postgres lifecycle automation: provisioning, failover, PITR, replicas, snapshots, hibernation — multiplied by many tenants.
**Decision:** Kubernetes (≥1.31) with the CloudNativePG operator; one CNPG `Cluster` per branch.
**Alternatives:** VM-per-tenant fleet (rebuild every operator feature ourselves), Patroni/Zalando operator (older ergonomics, weaker backup story), StackGres/KubeDB (licensing/ecosystem), Firecracker microVMs (isolation win; enormous platform build; Gen-2+ consideration), managed RDS-resale (no margin, no differentiation, no serverless).
**Consequences:** k8s operational competence is a standing requirement (R-5); CNPG's roadmap becomes ours to track; hibernation/snapshot features define Gen-1 serverless limits (ADR-004).

## ADR-004 · Two-generation serverless strategy: CNPG now, evaluate Neon OSS storage engine later — `accepted`
**Context:** True Neon-class serverless (sub-second cold start, instant CoW branches, PITR at page level) comes from their storage/compute separation (pageserver/safekeepers) — a distributed storage engine. Building one is a multi-year project; adopting Neon's (Apache-2.0) is possible but operationally heavy.
**Decision:** **Gen 1 (Phases 2–7):** CNPG + hibernation for scale-to-zero (seconds-scale wake), CSI snapshots + WAL PITR for branching/restore. **Gen 2 (Phase 8):** structured evaluation of self-hosting Neon's storage engine behind the same public API; adopt only if the written evaluation clears cold-start, ops-burden, and cost bars.
**Alternatives:** build a pageserver (rejected: R-1), adopt Neon OSS from day one (rejected: two distributed systems before first customer), never (rejected: caps the "serverless" claim long-term).
**Consequences:** Gen-1 wake latency is honestly documented (R-3); public API is storage-agnostic by design so Gen-2 is invisible to tenants.

## ADR-005 · Substrate for `syd1`: managed Kubernetes + cloud object storage/KMS — `accepted`
**Context:** DEPLOYMENT_ARCHITECTURE §1 options: managed cloud k8s vs bare-metal/colo (Nimbus's own docs plan a Sydney colo) vs hybrid. Cost vs operational-load trade.
**Decision:** **Owner (2026-07-17) approved the recommendation: managed k8s + cloud object storage/KMS** in ap-southeast-2 for launch; colo becomes a later cell (the cell model makes this placement, not migration). Concrete provider pick (EKS vs AKS vs GKE) is a Phase 1 Terraform-module decision made on quota/pricing at bootstrap time — the manifests above the k8s API are identical.
**Consequences:** Terraform/module structure must stay provider-portable; CSI-snapshot capability is a hard requirement on any pick.

## ADR-006 · One CNPG cluster per branch (no shared-instance tenancy for tenant data) — `accepted`
**Context:** Density vs isolation. Shared instances (schema-per-tenant) are denser but leak (extensions, roles, crash blast radius, noisy neighbours, per-tenant PITR impossible).
**Decision:** every branch = its own Postgres cluster; density comes from small CUs + scale-to-zero, not co-tenancy.
**Consequences:** higher floor cost per branch (R-10); clean per-branch PITR/restore/metrics; simpler security story (MULTI_TENANCY §2).

## ADR-007 · Custom `pg-gateway` for TCP ingress — `accepted`
**Context:** Requirements: TLS+SNI routing to per-branch services, wake-on-connect hold for scale-to-zero, per-endpoint caps/metering, startup-parameter fallback routing. Traefik v3 does Postgres STARTTLS SNI routing but cannot hold-and-wake; per-branch LoadBalancers explode cost.
**Decision:** small Go proxy: parse startup/TLS, route, hold, count — **no protocol rewriting**; Traefik remains for HTTP only.
**Consequences:** we own a data-path component (R-7): fuzzing, soak tests, canaries mandatory; it becomes the natural insertion point for Gen-2 features (quotas, read-write splitting) later. ADR-007 defines the *hold*; the wake *signal* transport is decided separately in ADR-014 (a single coalesced POST to the control-plane API — a bounded expansion of this "route, hold, count" scope, not a general API client or DB access).

## ADR-008 · Compute stays Nimbus's domain; NimbusDB does databases — `accepted`
**Context:** Roadmap lists serverless compute/functions; Nimbus already is the compute/hosting product with `site` and `agent` kinds.
**Decision:** NimbusDB never runs tenant application compute. "Deploy Compute/API/Worker/Cron/Frontend" actions drive Nimbus via its REST API; workloads and databases stay loosely coupled (soft links, detachable) per SYSTEM_ARCHITECTURE §7.
**Consequences:** no duplicated deploy pipeline; Phase 6 depends on Nimbus's API stability (R-11); worker/cron ride Nimbus's `agent` kind until Nimbus adds first-class kinds (tracked with Nimbus).

## ADR-009 · Console adopts Nimbus design language; P2E handoff adopted as token/handoff format — `accepted`
**Context:** No console design export provided; two systems found (Nimbus dark/forest/blue; Prompt2Eat cream/amber — a *product* brand, wrong for platform chrome). The P2E bundle demonstrates the intended handoff format (`tokens.css` + state catalogue).
**Decision:** per DESIGN_SYSTEM_MAPPING.md — Nimbus tokens as interim base, token-isolated so a future export re-skins cheaply; future exports expected in the P2E bundle format under `/docs/design/`.
**Consequences:** visual coherence with the hosting platform now; R-15 contained. **Owner (2026-07-17) confirmed a dedicated console design export IS forthcoming** — the interim system proceeds until it lands per DESIGN_SYSTEM_MAPPING §5.

## ADR-010 · NATS JetStream for eventing; Temporal only from Phase 5 — `accepted`
**Context:** Need an event bus (state changes, usage samples, webhooks) from Phase 2, and durable multi-step workflows (imports) from Phase 5. Provisioning itself is reconciler-shaped, not workflow-shaped.
**Decision:** NATS JetStream early (single binary, replayable streams, work queues); Temporal introduced with the import engine, scoped to long-running human-gated workflows; reconcilers never depend on Temporal.
**Alternatives:** Kafka (heavy), Redis Streams (weaker durability), Temporal-for-everything (couples provisioning availability to Temporal), Postgres-queues-only (fine intra-service; not a bus).
**Consequences:** two async systems eventually — accepted because their roles don't overlap; both self-hosted on the platform cluster.

## ADR-011 · PostgreSQL 17 default / 16 supported; pooled+direct dual endpoints are launch scope — `accepted`
**Context:** Both first customers run Neon-Postgres with stock-16 semantics, additive-only Drizzle migrations, and hard dependencies on *both* pooled (web) and session-mode (pg-boss/migrations/interactive-tx) connections.
**Decision:** PG 17 default for new projects, 16 selectable; every branch ships `rw-direct` + `rw-pooled` (PgBouncer transaction mode with `max_prepared_statements`) from Phase 2. `ro-pooled` follows in Phase 4.
**Consequences:** migration runbooks (MIGRATION_STRATEGY §6–7) need no app re-architecture; pooling docs are a first-class support surface (R-4).

## ADR-012 · Spec-first OpenAPI 3.1 + generated clients — `accepted`
**Context:** Console, Nimbus integration, CLI, and external users all consume the API; hand-written clients drift.
**Decision:** `/api/openapi.yaml` is the contract; handlers and the TS client are generated/validated against it in CI; breaking-change linting (oasdiff) gates PRs.
**Consequences:** slightly slower first endpoint, permanently cheaper every consumer after.

## ADR-013 · Phase 1 authentication scope: API keys + one-time bootstrap; console sessions move to Phase 3 — `accepted`
**Context:** ROADMAP Phase 1 originally listed "console session + API keys". Console sessions
require the email (magic-link) infrastructure that belongs with the console itself (Phase 3,
matching both customer apps' Auth.js pattern); building it headless in Phase 1 adds surface
without a consumer.
**Decision:** Phase 1 ships `ndb_` API-key auth as the complete programmatic path, plus a
one-time bootstrap flow: if `NDB_BOOTSTRAP_TOKEN` is configured **and no org exists yet**,
`POST /v1/bootstrap` (presenting that token) creates the initial user, org, and owner API key.
Console session auth (magic-link, MFA path per SECURITY_MODEL §3) lands with the console in
Phase 3. ROADMAP Phase 1/3 scopes updated accordingly.
**Consequences:** Phase 1 gate ("an API key can create an org/project record") is fully testable
without email infra; no throwaway auth code.

## ADR-014 · Wake/suspend are desired-state flips through the branch state machine; the gateway triggers wake via the control-plane API, never a reconciler RPC or a direct DB write — `accepted`
**Context:** Phase 4 scale-to-zero needs a *wake-on-connect* trigger: when a client hits a
suspended endpoint the gateway holds the connection and something must un-hibernate the compute.
The docs were inconsistent about what that "something" is — the SYSTEM_ARCHITECTURE §2 mermaid drew
a direct `gateway → reconciler` edge, while design principle #2 and MASTER §7 ("everything
reconciles; no imperative fire-and-forget") forbid exactly that. ADR-007 scopes the gateway to
"route, hold, count" and is silent on the wake signal. Three transports were on the table:
(a) gateway → control-plane API HTTP call; (b) gateway writes/flips desired state directly in the
control-plane DB; (c) gateway → reconciler RPC.
**Decision:** Suspend and wake are **desired-state transitions on the branch**, converged by the
reconciler — the same shape as provisioning and teardown. A branch's single `state` column carries
two new *transitional* states: `suspending` (ready → suspending, reconciler hibernates the CNPG
cluster + scales the pooler to zero → `suspended`) and `resuming` (suspended → resuming, reconciler
un-hibernates + scales the pooler back → `ready`). Endpoints move in lockstep. The **gateway's
automatic wake** is a call to the control-plane API's resume action (transport **a**), which
performs the `suspended → resuming` flip; the reconciler observes and converges. We reject **(b)** —
giving a data-path component control-plane DB credentials is a large security/coupling expansion
(the gateway holds tenant bytes, per ADR-007/R-7) — and **(c)** — a fire-and-forget RPC violates
MASTER §7 and makes wake non-crash-safe. The transitional state *is* the durable desired-state
record, so a wake survives a reconciler restart. Wake is **coalesced per branch**: the flip is
idempotent (a resume on an already-`resuming`/`ready` branch is a no-op success), so a connection
storm produces at most one state change (SECURITY_MODEL §2). The same resume action serves the
human "resume" API call, the gateway's on-connect wake, and Nimbus's deploy-time prewarm ping
(R-3) — one path, one idempotency.
**Alternatives:** direct DB flip (rejected — credentials/coupling); reconciler RPC (rejected —
not crash-safe, violates reconciliation model); a separate `desired_compute_state` column
(rejected — the existing transitional-state pattern already models desired state and the reconciler
already only acts on transitional states, so reusing it is simpler and consistent).
**Consequences:** the gateway gains exactly one outbound dependency — a single authenticated,
coalesced POST to the control-plane API (a bounded, reviewed expansion of ADR-007's scope, tracked
under R-7), not a full API client or DB access. The wake path is `gateway → control-plane API →
control-plane DB → reconciler`, which fixes those components as the highest-availability tier
(SYSTEM_ARCHITECTURE §5): during a full control-plane outage, already-running databases keep
serving (routes are cached in the gateway) but *suspended* branches cannot wake — an honest,
documented limitation (R-3). Hitting the p95 < 25 s wake SLO requires the reconciler to observe the
`resuming` flip promptly (short reconcile interval / event-driven wake), noted for the Phase 4 wake
implementation. This increment lands the control-plane spine (state machine + reconciler
hibernation + route mapping); the gateway hold-and-wake and the idle-suspend detector are the
follow-on increments that build on it. Supersedes the §2 mermaid's direct gateway→reconciler edge,
which is redrawn to `gateway → API`.

**Addendum (concrete transport).** The gateway's wake call targets a dedicated PRIVILEGED,
cross-tenant endpoint `POST /internal/branches/{br}/wake`, not the org-scoped tenant
`POST /branches/{br}/resume` — the gateway serves every tenant and cannot present an org-scoped
credential. The endpoint resolves the branch's org internally and performs the same idempotent
`suspended → resuming` flip, so the human resume and the gateway wake converge through one
transition. It is authenticated by a shared `NDB_GATEWAY_TOKEN` bearer (interim, pending mTLS —
SECURITY_MODEL §3) and disabled when the token is unset. The route table gains a `branch_id` field
so the gateway knows which branch to wake for a connecting endpoint. This increment adds the
endpoint + route field; the gateway-side hold/coalesce/poll logic is the next increment.

## ADR-015 · Suspend-on-idle is decided by the control plane from gateway-reported activity, aggregated across all gateway replicas, and is fail-safe — `accepted`
**Context:** Scale-to-zero's *suspend* half needs to detect that a branch is idle and flip it
`ready → suspending` (ADR-014's state machine then hibernates it). The obvious source is the
gateway's per-endpoint connection counters — but the gateway runs as **multiple replicas**
(DEPLOYMENT §3: 3 nodes behind one L4 LB). Any single gateway sees only *its own* connections, so a
gateway that suspended a branch based on its local view would kill live connections held by a
*different* gateway. Wake tolerates redundant triggers; suspend does not — a wrong suspend is a
data-plane outage for that tenant.
**Decision:** the suspend decision is made by the **control plane**, never by a gateway. Each
gateway periodically reports its per-branch active-connection counts to a privileged internal
endpoint `POST /internal/gateway-activity` (same `NDB_GATEWAY_TOKEN` auth as wake). The control
plane stores them per `(branch, gateway)` in a `branch_activity` telemetry table and maintains
`branches.last_active_at` (bumped whenever any gateway reports activity, and set when a branch
becomes ready/resumes — so a freshly-ready branch gets a full grace period). An idle sweep runs in
the reconciler loop: a ready branch with `suspend_timeout_s > 0` is flipped to `suspending` only
when its **globally aggregated** active count (summed across all *recently-reporting* gateways) is
zero AND `last_active_at` is older than its `suspend_timeout`. The sweep is **fail-safe**: if *no*
gateway has reported within the staleness window it does nothing at all — the platform never
mass-suspends when activity reporting is down, undeployed, or a gateway has just started. Stale
reports (from a crashed/rolled gateway) age out of the aggregation window and are ignored.
**Alternatives:** gateway-unilateral suspend (rejected — unsafe under multiple replicas, the core
reason for this ADR); reconciler polling every tenant's `pg_stat_activity` (rejected — needs
per-tenant DB credentials + a connection to every cluster each interval, heavy and couples the
control plane to the data path); a shared cache (Redis) of activity (deferred — the control-plane
DB is already the aggregation point and avoids a new dependency).
**Consequences:** suspend latency is bounded by the report interval + reconcile interval (tens of
seconds), which is fine — idleness is measured in minutes. `suspend_timeout_s = 0` disables
autosuspend for a branch (paid-plan opt-out, MIGRATION_STRATEGY/DATABASE_ARCHITECTURE §7). The
`branch_activity` table is ephemeral telemetry (no FK, orphan rows are harmless and ignored by the
ready-only sweep). This reuses the gateway↔control-plane auth + the ADR-014 state machine; the only
new privileged mutation is the idle `ready → suspending` flip performed inside the reconciler.

## ADR-016 · A branch is a data fork: non-root branches bootstrap by CNPG recovery from the parent's WAL archive — `accepted`
**Context:** "Branching" must give a new branch its parent's data (like Neon), optionally at a
point in time (`POST /branches {from_branch, at?}`). Options for the copy: CNPG volume snapshots
(CoW, fast, storage-class-dependent), `pg_basebackup` streaming from the live parent (needs the
parent up + replication certs, adds load to the primary), or **recovery bootstrap from the parent's
barman WAL archive** (the same archive we already keep for PITR/backups).
**Decision:** every non-root branch is a **data fork** provisioned by CNPG `bootstrap.recovery` from
its parent's WAL archive (an `externalClusters` origin pointing at the parent's barman object
store). "Branch from now" recovers to the latest archived WAL; "branch at `T`" sets
`recoveryTarget.targetTime = T` (`branches.bootstrap_at`, immutable — a bootstrap parameter). The
child gets its **own** compute spec and its **own** forward WAL-archive stream (a distinct
`destinationPath`), so the fork is fully independent of the parent afterwards; the parent's archive
is read-only to it. The project's default branch `main` is the sole **root** (`parent_id` NULL) and
bootstraps `initdb` (empty). This reuses the Phase 2e recovery-cluster builder — branching is the
third consumer alongside restore-verification and instant-restore.
**Alternatives:** volume snapshots (deferred to Gen-2 / a storage-class that supports them — the
fast CoW path, but not portable across substrates); `pg_basebackup` from the live parent (rejected
for v1 — loads the primary and needs replication-cert plumbing, whereas the WAL archive is already
there and read-only). Both remain open as faster paths later; the API contract (`from_branch`,
`at`) is copy-mechanism-agnostic.
**Consequences:** branching **requires** the parent to have a WAL archive — so it needs
`BackupConfig` (always set in staging/prod; in local dev without backups a branch falls back to an
empty `initdb` cluster with a logged warning). Branch-create latency is a full recovery (seconds to
minutes by size) — the honest Gen-1 number; CoW snapshots are the Gen-2 speedup. A branch created
`at` a time before the parent's retention window will fail recovery (surfaced as the branch going
`error`); the API documents the retention bound.

---

## Open questions — **all answered by owner, 2026-07-17**

| # | Question | Decision |
|---|---|---|
| Q1 | Product name | **NimbusDB** ("use it for now"; revisit pre-GA) — ADR-001 |
| Q2 | Substrate for `syd1` | **Managed k8s + cloud object storage/KMS** (recommendation approved) — ADR-005 |
| Q3 | Dedicated console design export intended? | **Yes — forthcoming.** Interim Nimbus-derived system until it lands (DESIGN_SYSTEM_MAPPING §5) — ADR-009 |
| Q4 | Billing processor | **Stripe** (Phase 7) |
| Q5 | Migration order | **Roster first, then Prompt2Eat** (MIGRATION_STRATEGY §7) |
