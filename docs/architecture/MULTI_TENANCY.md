# Multi-Tenancy Model — Zale DB

**Status:** Draft v0.1

---

## 1. Tenancy hierarchy

```
Organization (billing + isolation boundary)
 └── Members (users with roles) / API keys (org-scoped, least-privilege scopes)
 └── Project (unit of provisioning; maps to one app/service)
      └── Branch (main = production; previews/dev branches)
           └── Endpoints, Databases, Roles
```

- **Organization** is the tenant. Billing, quotas, API keys, audit trails, and Nimbus links
  hang off the org. Personal orgs are auto-created at signup (same UX shape as Nimbus/Vercel).
- **Project ↔ application.** Prompt2Eat and Roster are each one project in one org.
- **Branch = environment.** `production` / `preview` / `development` roles; matches how both
  customer apps already separate prod (Neon main) from dev (docker-compose), and how Nimbus
  targets env vars (`production,preview,development`).

Mapping to Nimbus for Phase 6: Nimbus `team` ↔ Zale DB `org` (soft link), Nimbus `project` ↔
Zale DB `project` (soft link). Links are stored as IDs+URLs on both sides, no cross-system
foreign keys; either side can detach (SYSTEM_ARCHITECTURE §7).

## 2. Isolation layers (defence in depth)

| Layer | Mechanism | Notes |
|---|---|---|
| **Compute** | One CNPG cluster (dedicated Postgres processes) per branch; per-namespace `ResourceQuota` + `LimitRange`; pod `securityContext` (non-root, seccomp, no privilege escalation) | No shared Postgres instances between tenants, ever (ADR-006). |
| **Kubernetes** | Namespace per project (`prj-<ulid>`) | All branch resources live inside it; namespace deletion = project teardown (after backup retention hold). |
| **Network** | Cilium default-deny; allow: gateway→pooler/cluster, cluster→object storage (WAL), metrics scrape. No tenant-to-tenant path, no tenant→control-plane path. | Egress from tenant Postgres pods is denied except WAL archive + declared FDW allowlist (off by default). |
| **Storage** | PVC per cluster; encrypted at rest; object-storage prefixes per project with scoped credentials (per-namespace access via workload identity) | A tenant's WAL/backups are unreadable by another tenant's pods. |
| **Identity** | Tenant DB roles exist only inside their branch; control-plane API scopes every query by `org_id`; API keys carry explicit scopes (`projects:read`, `branches:write`, …) | No global roles in tenant clusters. |
| **Control plane data** | Repository layer injects `org_id` predicates (the discipline Nimbus's `PROJECT_ACCESS` predicate already demonstrates) **plus** Postgres RLS policies on org-scoped tables as a second net | RLS enabled with `app.current_org` set per request transaction. |

## 3. Noisy-neighbour controls

- CPU/memory requests=limits for tenant Postgres pods (guaranteed QoS) — a tenant can't burst
  into another's headroom beyond its CU ceiling.
- IO: storage-class IOPS budgets per volume where the substrate supports it; per-branch
  `max_connections` and gateway-enforced connection caps; statement timeout defaults
  (overridable per role by the tenant).
- Gateway per-endpoint rate/connection limits protect the shared L4 layer.
- Scheduler spread: production-tier primaries spread across nodes/AZs (topology constraints);
  dev-tier branches pack densely (they're the scale-to-zero population).
- Escalation path: "cells" — dedicated node pools (or dedicated clusters) for large/regulated
  tenants, selected by org plan, invisible in the API (§6).

## 4. Control-plane authorization model

- **Users** authenticate to the console (OIDC/email — SECURITY_MODEL §3); org membership roles:
  `owner` (billing, delete, member admin), `admin` (all resources), `member` (create/use
  branches, no destructive prod ops), `viewer` (read-only).
- **API keys**: org-scoped, hashed at rest (SHA-256, `zdb_` prefix — mirrors Nimbus's `nbt_`
  pattern), explicit scope list, optional expiry, revocable, last-used tracking. Keys never
  grant console login.
- **Service integrations** (Nimbus): a dedicated key kind with only the scopes the integration
  needs (`projects:provision`, `endpoints:read`, `webhooks:manage`).
- Every mutating request writes an `audit_log` row (actor, action, target, IP) — append-only.

## 5. Tenant lifecycle

| Event | Behaviour |
|---|---|
| Org created | Personal org + default quotas (plan `free`). |
| Project created | Namespace + quotas + `main` branch provisioned (SYSTEM_ARCHITECTURE §3.1). |
| Payment failure / plan downgrade | Grace period → suspend non-production branches → suspend all (storage retained per retention policy). Never silent data deletion. |
| Project deleted | Endpoints revoked immediately; final backup taken; namespace deleted; backups retained `deletion_retention` (default 7 days) then purged; audit trail retained. |
| Org deleted | All projects per above + billing closure; audit log retained per compliance policy (SECURITY_MODEL §7). |

## 6. Scaling tenancy: cells

The unit of horizontal platform growth is a **cell**: one Kubernetes cluster + gateway fleet +
observability stack serving a bounded set of orgs. Region `syd1` starts with cell `syd1-a`.
Placement is recorded per project (`projects.region`, `projects.cell`). The API namespace hides
cells (endpoints already encode region only). This bounds blast radius (a cell-wide incident
affects only its orgs), keeps upgrade waves small, and gives large tenants a dedicated-cell
option later without API changes.

## 7. What tenants can never do

- Reach another tenant's endpoints, backups, metrics, or logs.
- Obtain superuser, file-system access, or arbitrary extensions in their Postgres.
- Exhaust a shared pooler (poolers are per-branch, not shared).
- See platform-internal identifiers beyond their own resources in API/audit responses.
