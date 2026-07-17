# Master Implementation Plan — NimbusDB

**Status:** Draft for approval — v0.1 (Phase 0 deliverable)
**Owner:** CTO / Principal Architecture Group
**Last updated:** 2026-07-17
**Repository:** `zenulbashar/DB` (platform monorepo)

---

## 1. Mission

Build a production-grade, multi-tenant **serverless PostgreSQL platform** — comparable to Neon,
Supabase, and Railway's managed Postgres — that:

1. Serves as the managed-database layer for the existing **Nimbus** hosting platform
   (`zenulbashar/hosting`), remaining loosely coupled to it.
2. Migrates the first two customers off Neon completely: **Prompt2Eat**
   (`zenulbashar/order-tool`) and **Roster** (`zenulbashar/roster-tool`).
3. Once stable, onboards external tenants migrating from Neon, Supabase, Railway, AWS RDS,
   Azure Database for PostgreSQL, and self-hosted PostgreSQL.

**Working product name:** `NimbusDB` — provisional, chosen to sit inside the existing Nimbus
brand family (`nimbus.app`, token prefix `nbt_`). Company domain observed: `zaleit.com.au`.
Renaming is a find-and-replace-level decision recorded in [DECISION_LOG.md](DECISION_LOG.md) (ADR-001);
nothing in the architecture depends on the name.

---

## 2. Inputs analysed (Phase 0 discovery — completed)

| Input | Source | Key facts that shape this plan |
|---|---|---|
| Platform repo | `zenulbashar/DB` | Empty greenfield repo; this monorepo hosts everything below. |
| Hosting platform "Nimbus" | `zenulbashar/hosting` | Next.js 15 + React 19 + Tailwind v4 + SQLite control plane. Data plane is **simulated** behind a `DeploymentDriver` interface (`src/lib/deploy-engine.ts`). Workload kinds: `site`, `agent`. REST API with session cookies + `nbt_` bearer tokens, deploy hooks, teams/projects tenancy, env-var injection per project, region model (`syd1`), mocked billing (hobby/pro). Its own docs name Postgres, k8s/Firecracker, and Caddy/Traefik as intended real infra. |
| Roster | `zenulbashar/roster-tool` | Next.js 16, Drizzle + plain `pg` TCP pool. Neon Sydney (`ap-southeast-2`): **pooled** endpoint for the Vercel web app (`syd1`), **direct session-mode** endpoint for the Railway worker running `pg-boss` and for CI migrations (`PROD_DATABASE_URL`). ~40 tables, 32 additive migrations, **no extensions**, Auth.js **database sessions**, clock photos stored as `bytea`. |
| Prompt2Eat | `zenulbashar/order-tool` | Next.js 16, Drizzle + `@neondatabase/serverless` **WebSocket driver** (needs driver swap), interactive transactions (needs session-capable pooling or direct), ~41 tables, 55+ additive migrations, ~28 `pgEnum`s, **no extensions**, app-generated UUIDs, files in Cloudflare R2 (not in DB), Vercel `syd1` + Vercel Cron + Stripe webhooks. |
| Design export | `order-tool/design/design_handoff_prompt2eat/` | High-fidelity design-handoff bundle (tokens.css, tailwind.theme.js, component state catalogue). This is the **Prompt2Eat** design system, not a console design for NimbusDB. See [DESIGN_SYSTEM_MAPPING.md](DESIGN_SYSTEM_MAPPING.md) for how the console handles this. |

**Two decisive discoveries:**

1. **Nimbus is a working control plane with a simulated data plane.** Integration with Nimbus is
   therefore an API/contract integration (real today), while "deploy compute next to your DB"
   buttons will drive Nimbus's control plane, whose machine layer is being built separately.
   NimbusDB must not depend on Nimbus's data plane existing. (Risk R-11.)
2. **Both first customers are already portable.** Neither uses Postgres extensions or
   Neon-proprietary storage features. Roster needs zero code changes (env swap only);
   Prompt2Eat needs a one-file driver swap. Both require the **pooled + direct dual-endpoint
   model**, which is therefore a launch requirement, not a nice-to-have.

---

## 3. Architecture summary (canon)

Full detail in [SYSTEM_ARCHITECTURE.md](SYSTEM_ARCHITECTURE.md). The one-paragraph version:

A **Go control plane** (REST API + reconciler) manages tenant Postgres clusters run by
**CloudNativePG on Kubernetes** (PostgreSQL 17 default, 16 supported). Every project branch gets
a **direct endpoint** (session mode) and a **pooled endpoint** (PgBouncer, transaction mode).
A custom Go **pg-gateway** (wire-protocol-aware TCP proxy) routes connections by TLS SNI to the
right cluster and implements **wake-on-connect** for scale-to-zero. Continuous WAL archiving to
S3-compatible object storage gives **PITR, backups, and branch-from-point-in-time**; CSI volume
snapshots give fast **cloning/branching**. **NATS JetStream** carries platform events and usage
metering; **Temporal** orchestrates long-running workflows (imports, restores) from Phase 5.
Observability is **OpenTelemetry → Prometheus/Loki/Grafana**, with **ClickHouse** for usage
analytics and query insights at scale. The **console** is a Next.js app sharing Nimbus's design
language. Everything deploys via **GitHub Actions → ArgoCD (GitOps)**, with **Terraform** for
cloud-level resources, **Cilium** CNI for tenant network isolation, and **Traefik** for HTTP ingress.

**Serverless strategy (two-generation plan, ADR-004):** Generation 1 ships proven, boring
technology — CNPG clusters with hibernate/resume scale-to-zero (cold start seconds-to-tens-of-seconds)
and snapshot-based branching. Generation 2 evaluates adopting the **open-source Neon storage engine**
(pageserver/safekeepers, Apache-2.0) for sub-second cold starts and instant copy-on-write branching.
We do not rebuild a storage engine from scratch, and we do not block launch on Neon-level cold starts.

---

## 4. Monorepo layout (to be created in Phase 1)

```
/docs/architecture/          ← this plan (source of truth; docs-first rule below)
/docs/runbooks/              ← operational runbooks (created per phase)
/services/control-plane/     ← Go: REST API, auth, reconciler, metering ingest
/services/pg-gateway/        ← Go: Postgres wire-protocol TCP gateway (SNI routing, wake-on-connect)
/services/import-engine/     ← Go: migration/import workers (Phase 5, Temporal activities)
/console/                    ← Next.js console (SQL editor, dashboards, admin portal)
/packages/api-client/        ← generated TypeScript client from OpenAPI spec
/deploy/terraform/           ← cloud bootstrap (cluster, DNS, object storage, KMS)
/deploy/k8s/                 ← Helm charts / Kustomize for platform components
/deploy/argocd/              ← ArgoCD app-of-apps
/api/openapi.yaml            ← OpenAPI 3.1 contract (generated clients; spec-first)
/tools/                      ← dev scripts, local kind-cluster bootstrap
CHANGELOG.md
```

Rationale: single repo keeps the API contract, control plane, gateway, and console atomic per
change; Go services and the TS console have independent CI pipelines within it (path-filtered).

---

## 5. Phase plan

Phases are sequential gates. **A phase is done only when its exit checklist passes** (§6).
Detailed milestones and scope per phase live in [ROADMAP.md](ROADMAP.md); this table is the
authoritative ordering.

| Phase | Name | Outcome |
|---|---|---|
| **0** | Planning & architecture (this document set) | 12 architecture docs committed; internally consistent; approved by owner. |
| **1** | Foundations & control plane core | Monorepo scaffold, CI, local kind cluster, control-plane API skeleton (orgs/projects/API keys/auth), control-plane Postgres, OpenAPI spec, ArgoCD bootstrap. |
| **2** | Managed Postgres v1 (data plane) | CNPG-provisioned project databases with direct + pooled endpoints, TLS, per-tenant namespaces, backups + PITR to object storage, pg-gateway routing (no scale-to-zero yet). |
| **3** | Console v1 & developer surface | Console (projects, connection strings, roles, SQL editor, metrics dashboards, secrets/connection-string management), audit log, database import/export (dump/restore). |
| **4** | Elastic compute | Scale-to-zero (hibernate + wake-on-connect), compute autoscaling, read replicas, branching & cloning v1 (snapshot/PITR-based), instant restore. |
| **5** | Migration engine & first-customer cutover | Import engine (dump/restore + logical-replication modes), Neon/Supabase/RDS/Azure source adapters, Temporal workflows, **Roster and Prompt2Eat migrated to production**. |
| **6** | Nimbus integration | Provider contract in Nimbus (`DatabaseDriver`-idiom), deploy-compute/API/worker/cron/frontend actions, env-var injection, attach/detach workload, shared billing/usage surface. |
| **7** | Commercial readiness | Usage metering pipeline → billing, quotas/plans, admin portal, query insights (pg_stat_statements → ClickHouse), SOC2 control mapping, external-tenant onboarding. |
| **8** | Scale & serverless Gen 2 | Multi-region readiness, cell-based isolation for large tenants, evaluation/adoption of Neon OSS storage engine, roadmap modules (queues, cron, object storage, realtime, vector, AI gateway…). |

Phases 3 and 4 may overlap in staffing but gate independently. Phase 5 (first-customer migration)
is deliberately **before** Nimbus integration: proving the database product on real production
workloads matters more than platform integration polish.

---

## 6. Phase exit checklist (applies to every phase)

Per the implementation strategy, each phase must, before merging its final PR:

1. **Docs updated first** — if implementation diverged from these documents, the documents are
   amended in the same PR *before* code review ("docs-first rule"). DECISION_LOG.md gets an ADR
   for every material deviation.
2. **Tests** — unit + integration tests for new surface; data-plane changes require a
   destructive-restore test (backup taken, cluster destroyed, restore verified) in CI or staging.
3. **Self-review** — a written review pass in the PR description from each of these lenses:
   Principal Engineer (correctness/simplicity), Security Architect (authn/z, secrets, blast
   radius), SRE (failure modes, observability, runbooks), Database Engineer (durability,
   pooling semantics), Staff Frontend (console UX/accessibility) — only the lenses the phase touches.
4. **Performance review** — for data-path changes: connection latency, wake latency, and
   restore-time measurements recorded in the PR.
5. **CHANGELOG.md** entry appended.
6. **Commit & push** to the designated branch; PRs only after CI is green.

---

## 7. Working agreements

- **Docs are the source of truth.** Code that contradicts `/docs/architecture` is a bug in one
  of the two; the docs are corrected first, then the code.
- **Spec-first API.** `api/openapi.yaml` changes precede handler implementation; the TS client
  is generated, never hand-written.
- **Boring on the data path.** Anything that holds customer bytes (Postgres, WAL archive,
  backups) uses proven upstream technology with our automation around it. Custom code is
  reserved for control-plane logic and the gateway.
- **Everything reconciles.** Desired state lives in the control-plane DB; reconcilers converge
  Kubernetes to it. No imperative "fire and forget" provisioning.
- **No secrets in git, ever.** See [SECURITY_MODEL.md](SECURITY_MODEL.md).
- **Tenant data is radioactive.** No tool, log line, or metric may contain tenant row data.
  Query insights store normalized query shapes, not parameters.

---

## 8. Document index

| Document | Contents |
|---|---|
| [SYSTEM_ARCHITECTURE.md](SYSTEM_ARCHITECTURE.md) | Component architecture, control/data plane, request & lifecycle flows, technology justification. |
| [DATABASE_ARCHITECTURE.md](DATABASE_ARCHITECTURE.md) | Tenant Postgres topology (CNPG), storage, WAL/PITR, branching, replicas, pooling; control-plane schema. |
| [MULTI_TENANCY.md](MULTI_TENANCY.md) | Tenancy hierarchy, isolation layers (namespace/network/storage/identity), noisy-neighbour controls. |
| [ROADMAP.md](ROADMAP.md) | Phase-by-phase milestones, acceptance criteria, long-term module roadmap. |
| [API_SPECIFICATION.md](API_SPECIFICATION.md) | REST API v1: resources, auth, representative schemas, error model, versioning. |
| [SECURITY_MODEL.md](SECURITY_MODEL.md) | Identity, authn/z, secrets, encryption, audit, SOC2 mapping, threat model. |
| [DEPLOYMENT_ARCHITECTURE.md](DEPLOYMENT_ARCHITECTURE.md) | Environments, k8s cluster layout, GitOps pipeline, IaC, DR, zero-downtime deploys. |
| [DESIGN_SYSTEM_MAPPING.md](DESIGN_SYSTEM_MAPPING.md) | Console design language, token layer, mapping to Nimbus primitives and the design-handoff format. |
| [RISK_REGISTER.md](RISK_REGISTER.md) | Ranked risks with mitigations and owners. |
| [MIGRATION_STRATEGY.md](MIGRATION_STRATEGY.md) | Import engine design; per-source adapters; Roster & Prompt2Eat cutover runbooks. |
| [DECISION_LOG.md](DECISION_LOG.md) | ADRs: every load-bearing decision, alternatives considered, status. |

---

## 9. Approval

Implementation (Phase 1) does **not** begin until the owner has reviewed this document set.
Open questions requiring owner input are collected in DECISION_LOG.md §"Open questions"
(product name, cloud/hosting substrate, PG default version, billing processor).
