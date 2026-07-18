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

---

## Open questions — **all answered by owner, 2026-07-17**

| # | Question | Decision |
|---|---|---|
| Q1 | Product name | **NimbusDB** ("use it for now"; revisit pre-GA) — ADR-001 |
| Q2 | Substrate for `syd1` | **Managed k8s + cloud object storage/KMS** (recommendation approved) — ADR-005 |
| Q3 | Dedicated console design export intended? | **Yes — forthcoming.** Interim Nimbus-derived system until it lands (DESIGN_SYSTEM_MAPPING §5) — ADR-009 |
| Q4 | Billing processor | **Stripe** (Phase 7) |
| Q5 | Migration order | **Roster first, then Prompt2Eat** (MIGRATION_STRATEGY §7) |
