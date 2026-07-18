# API Specification — NimbusDB REST API v1

**Status:** Draft v0.1 · The normative contract will live at `/api/openapi.yaml` (OpenAPI 3.1, spec-first — MASTER plan §7). This document fixes the resource model, auth, conventions, and representative shapes.

---

## 1. Fundamentals

- **Base URL:** `https://api.db.nimbus.app/v1` (region-agnostic; resources carry `region`).
- **Format:** JSON request/response; `Content-Type: application/json`.
- **IDs:** ULIDs with typed prefixes: `org_…`, `prj_…`, `br_…`, `ep_…`, `key_…`, `imp_…`, `bak_…`.
- **Pagination:** cursor-based — `?limit=` (≤100) & `?cursor=`; responses carry `next_cursor`.
- **Idempotency:** all POSTs accept `Idempotency-Key` header (stored 24 h).
- **Errors (RFC 9457 problem+json):**

```json
{ "type": "https://api.db.nimbus.app/errors/branch-not-ready",
  "title": "Branch is not ready", "status": 409,
  "detail": "Branch br_01J… is suspended and cannot be branched from until resumed.",
  "request_id": "req_01J…" }
```

- **Versioning:** path-versioned (`/v1`); additive changes without version bump; breaking changes require `/v2` + 12-month deprecation window.
- **Rate limits:** per-key token bucket; headers `X-RateLimit-Limit/Remaining/Reset`; 429 with `Retry-After`.
- **Async operations:** long-running actions return `202` with an `operations` resource (`op_…`) to poll; also emitted as webhook events.

## 2. Authentication & authorization

| Credential | Format | Use |
|---|---|---|
| Console session | httpOnly cookie | Console only (first-party). |
| API key | `Authorization: Bearer ndb_<64hex>` | Programmatic; org-scoped; explicit scopes. Stored hashed (SHA-256); shown once at creation. |
| Service integration key | same format, restricted scope set | Nimbus ↔ NimbusDB integration. |

Scopes (least privilege): `orgs:read|write`, `members:manage`, `projects:read|write|provision`,
`branches:read|write`, `endpoints:read`, `roles:read|write`, `backups:read|write`, `restores:write`,
`imports:read|write`, `metrics:read`, `audit:read`, `keys:manage`, `webhooks:manage`, `usage:read`.
(`orgs:write` and `members:manage` were added during Phase 1 implementation — org mutation and
membership management need scopes distinct from read access.)

## 3. Resource map

```
/orgs                                   GET, POST
/orgs/{org}                             GET, PATCH, DELETE
/orgs/{org}/members                     GET, POST, PATCH, DELETE
/orgs/{org}/api-keys                    GET, POST; DELETE /{key}
/orgs/{org}/webhooks                    GET, POST, PATCH, DELETE
/orgs/{org}/usage                       GET   (metered rollups by meter, period)
/orgs/{org}/audit-log                   GET

/projects                               GET, POST
/projects/{prj}                         GET, PATCH, DELETE
/projects/{prj}/connection-uri          GET   (assembled URI per endpoint/role/db; secret-bearing, audit-logged)
/projects/{prj}/branches                GET, POST            (POST body: {name, from_branch, at?: timestamp|lsn, role})
/branches/{br}                          GET, PATCH, DELETE   (PATCH: compute bounds, suspend_timeout, retention)
/branches/{br}/endpoints                GET, POST            (kind: rw_pooled|rw_direct|ro_pooled)
/branches/{br}/databases                GET, POST, DELETE
/branches/{br}/roles                    GET, POST, DELETE
/branches/{br}/roles/{role}/reset-password  POST             (returns new secret once)
/branches/{br}/backups                  GET, POST
/branches/{br}/restore                  POST                 ({target_time|lsn|backup_id, mode: new_branch|promote})
/branches/{br}/suspend | /resume        POST                 (compute state flips; branches:write; idempotent/coalesced. resume is also the gateway wake-on-connect trigger — ADR-014. Storage-agnostic: no CNPG/hibernation detail leaks to the client)
/branches/{br}/metrics                  GET                  (time-series slices for dashboards)
/branches/{br}/query-insights           GET                  (Phase 7)
/branches/{br}/sql                      POST                 (console SQL editor proxy; short-lived scoped exec)

/projects/{prj}/imports                 GET, POST            (source config; mode dump_restore|logical_replication)
/imports/{imp}                          GET                  (state machine + checkpoints)
/imports/{imp}/cutover                  POST                 (final sync + switch gate)
/imports/{imp}/abort                    POST

/operations/{op}                        GET

/integrations/nimbus                    GET, POST, DELETE    (org-level link: Nimbus URL + token ref)
/integrations/nimbus/deploy             POST                 ({workload: compute|api|worker|cron|frontend, project_ref, env_injection: bool})
/integrations/nimbus/attach | /detach   POST                 (soft links per SYSTEM_ARCHITECTURE §7)
```

Admin-portal endpoints live under `/admin/v1/*` on a separate hostname, separate authn (platform-operator SSO), never reachable with tenant keys (SECURITY_MODEL §6).

## 4. Representative schemas

**Project**
```json
{
  "id": "prj_01JZX8K3TQ",
  "org_id": "org_01JZX7Y2MB",
  "name": "prompt2eat",
  "region": "syd1",
  "pg_version": 17,
  "default_branch_id": "br_01JZX8K4AA",
  "nimbus_link": { "nimbus_project_id": "…", "attached_at": "2026-09-01T02:11:00Z" },
  "created_at": "2026-09-01T02:10:31Z"
}
```

**Branch**
```json
{
  "id": "br_01JZX8K4AA",
  "project_id": "prj_01JZX8K3TQ",
  "name": "main",
  "role": "production",
  "state": "ready",
  "parent": null,
  "compute": { "min_cu": 0.25, "max_cu": 2, "suspend_timeout_s": 300, "current_cu": 0.5 },
  "storage_bytes": 734003200,
  "pitr_window": { "from": "2026-08-25T00:00:00Z", "to": "now" },
  "endpoints": [
    { "id": "ep_01JZX8K5RW", "kind": "rw_pooled",  "host": "ep-01jzx8k5rw.syd1.db.nimbus.app", "state": "ready" },
    { "id": "ep_01JZX8K5RD", "kind": "rw_direct",  "host": "ep-01jzx8k5rd.syd1.db.nimbus.app", "state": "ready" }
  ]
}
```

**Import**
```json
{
  "id": "imp_01JZY0A1BC",
  "project_id": "prj_01JZX8K3TQ",
  "source": { "kind": "neon", "host": "…redacted-ref…", "database": "neondb", "credential_secret": "sec_…" },
  "mode": "logical_replication",
  "state": "live_sync",   // preflight|schema_copy|initial_copy|live_sync|cutover_ready|cut_over|verified|failed|aborted
  "lag_bytes": 1024,
  "checkpoints": { "tables_copied": 41, "tables_total": 41, "checksum_ok": true },
  "created_at": "2026-10-02T21:00:00Z"
}
```

**Webhook event envelope** (HMAC-SHA256 signature header `X-NimbusDB-Signature`):
```json
{ "id": "evt_01…", "type": "branch.ready", "org_id": "org_…", "at": "…",
  "data": { "branch_id": "br_…", "project_id": "prj_…" } }
```
Event types v1: `project.provisioned|deleted`, `branch.ready|suspended|resumed|deleted`,
`endpoint.rotated`, `backup.completed|verification_failed`, `restore.completed`,
`import.state_changed`, `usage.threshold_reached`.

## 5. Connection-string surfacing rules

- `GET /projects/{prj}/connection-uri?branch=…&endpoint=rw_pooled&role=…&database=…` assembles
  the URI server-side; every call is audit-logged; response is never cached; console masks by
  default. Password material is returned **only** by explicit reset/creation calls (shown once) —
  reads return URIs with `:****@` unless `?reveal=true` (scope `roles:write` required, audited).

## 6. SQL editor proxy (`POST /branches/{br}/sql`)

Console-only scope. Server issues itself a short-lived branch role (or uses a session-scoped
pooled connection) with: `statement_timeout` (10 s default), row cap (10k), result size cap
(5 MB), single statement per call, EXPLAIN allowed, COPY blocked. Full statement text is
audit-logged (statement text is tenant-authored, treated as sensitive, retained 30 days).
Rationale: the browser never holds durable DB credentials.

## 7. Compatibility posture

Where a concept has an obvious Neon analogue (projects/branches/endpoints, pooled vs direct),
we keep the shapes close enough that migration tooling and tenant mental models transfer, but we
do **not** implement Neon's API verbatim (their API is versioned around their storage internals).
A `neonctl`-style CLI is a Phase 7 nice-to-have; the generated TS client (`/packages/api-client`)
is the first-class SDK from Phase 1.
