# Changelog

All notable changes to this repository. Format loosely follows [Keep a Changelog](https://keepachangelog.com/); one entry per phase gate plus notable intermediate merges.

## [Admin console — operator UI] — 2026-07-20

The operator UI over the admin API (ADR-018): see the whole platform, see which tenant uses what,
and fix tenant issues — from a browser.

### Added
- **Two shells, one app** — the console is restructured into route groups: the tenant console
  keeps the forest brand chrome under `(console)/`; **`/admin`** gets a visually distinct neutral
  "operator" chrome (DESIGN_SYSTEM_MAPPING §4's admin-portal note, realized early). Sessions are
  fully separate: the operator token lives in its own httpOnly cookie (`ndb_admin`, 12 h), and
  neither cookie opens the other surface.
- **Operator sign-in** (`/admin/connect`) — validates the pasted `NDB_ADMIN_TOKEN` against
  `GET /admin/overview` before persisting; wrong tokens are rejected at the form.
- **Platform overview** (`/admin`) — stat tiles (orgs, users, projects, branches, allocated CU,
  active keys), branch/import state histograms (status dots + labels, no color-alone), a
  **needs-attention** panel (branches in `error`), the **tenants table** (per-org members,
  projects, branches, CU, keys, running imports, last activity), and the cross-tenant audit feed.
- **Branches** (`/admin/branches`) — every tenant branch with org/project context, state filter
  pills, and **fix actions** (suspend/resume/resize) that drive the audited admin endpoints. An
  `error` branch deliberately shows "no legal transition" instead of a button that would 409.

### Verified (live, real browser)
- Signed in through the operator form; dashboard rendered live platform data; **suspended a
  tenant branch through the UI** → backend flipped it `ready → suspending` via the tenant state
  machine, and the **tenant's own audit log** (fetched with the tenant key) shows
  `branch.suspend · system/platform_admin` — the tenant-visible-interventions property, end to end.
- Wrong operator token rejected at the form; `/admin` without a session 307s to `/admin/connect`;
  a tenant key 401s on `/admin`; the admin cookie does not open the tenant console.
- Tenant console + KB regression-checked after the route-group restructure; `tsc` + `next build`
  clean (26 pages).

## [Admin API — platform-operator surface] — 2026-07-20

The Phase 7 "admin portal" backend pulled forward as an honest v1 (ADR-018): the operator can see
the whole platform, see which tenant uses what, and fix tenant issues.

### Decided (ADR-018)
- `/v1/admin/*` is authenticated by a dedicated **`NDB_ADMIN_TOKEN`** (bootstrap/gateway-token
  pattern: constant-time compare, surface disabled entirely when unset). Tenant keys never open
  `/admin`; the admin token opens nothing else. Reads use the store's privileged path — tenant RLS
  stays intact for every tenant credential. **Fix actions are the tenant state machine, not a
  bypass**, and every one is written to the affected tenant's audit log
  (`actor_type: system`, `actor_id: platform_admin`) — operator interventions are tenant-visible.
  Interim until Phase 7 operator RBAC; tracked as **R-17**.

### Added
- **Reads:** `GET /admin/overview` (org/user/project/branch/endpoint/key totals, branch+import
  state histograms, **allocated CU** — the sum of effective CU over compute-holding branches);
  `GET /admin/orgs` (per-tenant inventory: members, projects, branches by state, endpoints, active
  keys, running imports, allocated CU, `last_active_at` from gateway activity);
  `GET /admin/branches?state=` (find stuck/error resources, with org+project context);
  `GET /admin/audit-log` (cross-org feed). v1 "usage" is deliberately inventory — metered usage
  arrives with the Phase 7 pipeline.
- **Fix actions:** `POST /admin/branches/{br}/{suspend,resume,resize}` — resolve the branch's org
  (`ResolveBranchOrg`, now also backing the gateway wake) and drive the same org-scoped
  transitions tenants use (identical 409 semantics).
- Spec-first: `api/openapi.yaml` admin tag + `adminToken` scheme + `AdminOverview`/`OrgUsage`/
  `AdminBranch` schemas; TS client regenerated. Store: `postgres/admin.go` (SQL aggregates) +
  `memory/admin.go` (mirror). Server: dedicated route group outside tenant auth.

### Tests
- Server (memory): tenant keys/wrong/empty tokens 401, disabled surface 401; overview + org-usage
  counts; state-filtered branch list with tenant context; fix actions drive the state machine
  (409 from provisioning, suspend/resume/resize flips) and land in the tenant's audit log as
  `system`/`platform_admin`; 400 on bad resize; 404 on unknown branch. Postgres integration:
  the same aggregates against real SQL incl. allocated-CU drop after suspend and seeded
  `last_active_at`. Full unit + integration suites green.

## [Phase 3 — in-product knowledge base] — 2026-07-20

Self-serve help for every shipped feature, in the console.

### Decided (ADR-017)
- KB articles are **repo-authored markdown** (`console/content/kb/*.md`, frontmatter: title /
  category / order / summary) rendered by the console at `/kb` — the docs-first rule applied to
  user docs: content ships and versions with the feature it documents. Publicly readable (help must
  be reachable when sign-in is the problem). Rendered server-side with `marked`; repo content only —
  user-generated markdown must never be routed through this path.

### Added
- **17 articles** covering every shipped feature: getting started, using the console, projects,
  branches & data forks (PITR `at`), endpoints ("which one do I use" + pooling caveats), connection
  strings & the audited reveal, roles & databases, scale-to-zero, compute sizing & resize, read
  replicas, backups & PITR (fork-first recovery workflow), imports (modes, state machine, human-gate
  cutover), API keys & scopes, orgs & members, audit log, API basics (RFC 9457 errors, idempotency,
  pagination), troubleshooting.
- **`/kb`** index — category grouping + client-side search; **`/kb/[slug]`** article pages
  (statically generated, hand-rolled `.kb-prose` typography on the token layer); persistent
  **Help** link in the console header. Slug input is allow-listed (`^[a-z0-9-]+$`) so the file
  loader can't traverse.

### Verification
- `tsc --noEmit` + `next build` clean (22 pages; all 17 articles SSG). Boot check: `/kb` serves
  without a session; search filters live (Playwright); unknown slug and traversal attempts 404.

## [Phase 3 — console end-to-end verification + smoke harness] — 2026-07-19

The three console increments (read surface, branch management, project creation) were verified
against a **real running stack** — not just compile/build — and that verification was made
reproducible.

### Verified (live)
- Stood up Postgres + the control-plane API (migrations auto-applied) + the console. Bootstrapped a
  platform, created projects via the API, and confirmed the **console renders the live data**:
  dashboard lists the projects, the detail page shows the seeded `main` branch with its endpoints and
  a **masked** connection string (`roster_owner:****@…`), the auth guard 307-redirects without a
  session, and a bad project id renders the 404 page.
- **Write path** exercised through a real browser (Playwright/Chromium): submitting the console's
  "New branch" form created a branch (`feature-x`) via the server action → control plane, which
  persisted with the correct parent and rendered on the revalidated page. The mutations appear in the
  audit log (`branch.create`, `project.create`).

### Added
- `tools/smoke-e2e.sh` + `make smoke` — reproducible end-to-end smoke test: build/run the
  control-plane, bootstrap, create a project via the API, then assert the console renders that live
  data (and redirects without a session). Needs a fresh `DATABASE_URL`; no Docker or k8s required.
- README: corrected the stale "Phase 0 — planning" status to reflect Phases 1–4, and added a
  **Local development** section (`make dev` / `console-dev` / `test` / `smoke`).

## [Phase 3 — console: project creation + one-time credential reveal] — 2026-07-19

Projects can now be created from the console — the last piece needed to go from zero to a
connectable database entirely in the UI.

### Added
- **New project flow** (`/projects/new`) — org selector (from `GET /orgs`), name, region, and PG
  version; `POST /projects`. A **New project** button anchors the dashboard header.
- **One-time credential reveal** — the create response returns the seeded owner role's password
  *exactly once* (per the API contract). The form swaps to a credentials panel (database, owner
  role, password) with copy buttons and a "shown exactly once" warning; the password lives only in
  the client component's state for that page view — never persisted to a cookie, URL, or storage —
  then links to the new project. Lose it → reset via the role API, not re-fetch.
- **`ButtonLink`** primitive — a `Link` styled as a button (shared base classes with `Button`),
  so link-actions don't nest `<button>` in `<a>`.

### Verification
- `tsc --noEmit` and `next build` clean (`/projects/new` route added). Boot check: `/projects/new`
  307-redirects to `/connect` without a session.

## [Phase 3 — console: branch management (first write surface)] — 2026-07-19

The console stops being read-only: branches can now be created and driven through their
lifecycle from the project detail page, against the existing control-plane endpoints.

### Added
- **Create branch** — a form on the project detail page (`POST /projects/{prj}/branches`): name,
  role, and the fork parent (`from_branch`, defaulting to the project's default branch — a branch is
  a data fork of its parent, ADR-016).
- **Branch lifecycle actions** — per-branch **Suspend** / **Resume** / **Resize** controls, wired to
  `POST /branches/{br}/{suspend,resume,resize}`. Which controls show is driven by the branch's
  current state (ready → suspend/resize; suspended → resume; transitional → "working…"), mirroring
  the control-plane state machine; a raced 409 still surfaces inline. Resize takes a CU value
  clamped to the branch's `min_cu`/`max_cu` bounds (the server clamps authoritatively).
- **Server actions** (`app/projects/[prj]/actions.ts`, `"use server"`) call the one typed client
  with the request's key and `revalidatePath` the detail page on success, so the new state renders
  without a manual refresh. Errors are normalized (`friendlyError`) and shown inline, never thrown to
  the error boundary.
- **Form primitives** — `Input`, `Select`, `Field` added to the design system (the §3 form-control
  gap's first slice), reused by the connect flow's styling idiom.

### Security / trust
- Action arguments (project/branch IDs) come from the client but confer no authority: every call is
  authorized by the caller's API key and RLS-scoped in the control plane, so a tampered ID can only
  reach resources the key already governs. Actions with no session cookie get a 401 and render an
  error (the page already redirects unauthenticated users).

### Verification
- `tsc --noEmit` and `next build` clean (detail route now bundles the client controls). Boot check:
  the detail route still 307-redirects to `/connect` without a session.

## [Phase 3 — console v1: live read surface + API-key connect] — 2026-07-19

The console stops being a static shell and reads live data from the control plane. This is the
Phase 3 read surface (ROADMAP Phase 3); write flows, the SQL editor, metrics, and email
magic-link sessions layer on next.

### Added
- **API-key connect flow** — the console signs in by validating a pasted `ndb_` key against the
  control plane (`listProjects`) and persisting it in an **httpOnly** cookie (`console/src/lib/session.ts`).
  This is the same credential the CLI/Nimbus integration use (ADR-013); email magic-link sessions
  (SECURITY_MODEL §3) are still deferred to a later Phase 3 slice. Server-side auth guard: every
  page redirects to `/connect` without a valid session; `Sign out` clears the cookie.
- **Live projects dashboard** (`/`) — server component reads `GET /projects` with the request's key
  and renders project cards (name, lifecycle `StatusDot`, PG version, region) linking to detail.
  Empty and error states are first-class (`EmptyState`, `ErrorNote`).
- **Project detail** (`/projects/{prj}`) — branch list with per-branch endpoints and compute bounds
  (`min–max CU`, current size), plus a **connection panel**: the masked connection URI assembled
  server-side via `GET /projects/{prj}/connection-uri` (never reveals a password — reveal stays the
  audited `roles:write` API path). One-click copy on connection strings and endpoint hosts (`CopyField`).
- **API client wiring** — one typed client for the whole console (ADR-012): `serverClient()` carries
  the caller's key from their cookie; `friendlyError()` normalizes `ApiError`/network failures into
  renderable text. No ad-hoc `fetch` in the console.
- **Design primitives** — `EmptyState`, `ErrorNote`, `CopyField` (mono value + copy button, secret
  masking), and the lifecycle `StatusDot` extended to all 8 resource states (transitional states
  pulse). `next.config.ts` transpiles the workspace `@nimbusdb/api-client`.

### Fixed
- **Spec-first contract gap** — `ResourceState` in `api/openapi.yaml` was missing `resizing` (the
  vertical-resize state shipped in the previous increment); a branch mid-resize is a state the API
  can return, so the enum now includes it and the TS client was regenerated. The console renders it.

### Tests / verification
- `tsc --noEmit` and `next build` clean (4 routes; `/` and `/projects/[prj]` dynamic). End-to-end
  boot check: `/` 307-redirects to `/connect` without a session; `/connect` serves the sign-in form.
- The API-client `api-contract` CI job (lint + client-staleness) covers the regenerated schema.

## [Phase 4 — vertical resize] — 2026-07-19

The zero-downtime vertical-resize substrate for compute autoscaling (ROADMAP Phase 4).

### Added
- **`current_cu`** (migration `0010`) — the branch's actual running compute size; the reconciler
  sizes the cluster's CPU/memory from it (`Compute.EffectiveCU`, falling back to `min_cu` before the
  first resize) instead of always `min_cu`.
- **Resize state machine** — `ResizeBranch(orgID, branchID, cu)` clamps `cu` to `[min_cu, max_cu]`
  and flips the branch `ready → resizing` (a new state); a new reconcile path (`resizeCompute` →
  `MarkBranchResized`) re-applies the cluster at the new size and flips it back to `ready` — the same
  zero-downtime, crash-safe shape as suspend/resume. Routing is untouched: endpoints stay `ready`
  and `resizing` branches remain in the route table. Idempotent at the same size; 409 from a
  non-ready/resizing state.
- **API** — `POST /branches/{br}/resize {cu}` (`branches:write`), the same action a metrics-driven
  autoscaler drives; `current_cu` surfaced on the Branch resource. Spec-first: `api/openapi.yaml`
  `resizeBranch` + `Compute.current_cu` + regenerated TS client.

### Notes
- This is the resize *mechanism*. The metrics-driven auto-decision (when to scale up/down from
  CPU/memory pressure) depends on the Phase 7 metrics pipeline (Prometheus → ClickHouse) and is
  deferred to it; the mechanism, bounds, and manual/autoscaler entry point ship now.

### Tests
- Reconciler: `BuildCluster` sizes from `current_cu` (with min fallback); a `resizing` branch
  re-applies the cluster at the new CU and marks resized when healthy. Store: `ResizeBranch`
  clamping, `ready → resizing → ready`, route stays up while resizing, conflict from a suspended
  branch, cross-org/missing 404 (postgres + memory). Server: `POST /resize` (409 on provisioning,
  400 on bad/absent cu, scope).

## [Phase 4 — branching / data forks] — 2026-07-19

### Decided (ADR-016)
- A branch is a **data fork**: every non-root branch (`parent_id` set) provisions its cluster by
  CNPG `bootstrap.recovery` from the **parent's WAL archive** (an `externalClusters` origin), not an
  empty `initdb`. The default branch `main` is the sole root (empty). Reuses the barman archive we
  already keep for PITR/backups — no new copy mechanism, and works on any substrate. CoW volume
  snapshots remain the Gen-2 speedup.

### Added
- **`BuildBranchedCluster`** — renders a fork with the **child's own** compute spec and **own**
  forward WAL-archive stream (a distinct `destinationPath`), bootstrapped by recovery from the
  parent's archive. `reconciler.clusterFor` routes a parented branch through it (and falls back to
  empty `initdb` in local dev without a backup config, with a logged warning).
- **Point-in-time forks** — `POST /branches {from_branch, at}` (RFC3339) sets
  `recoveryTarget.targetTime`; `at` requires `from_branch` and is stored immutably in
  `branches.bootstrap_at` (migration `0009`), surfaced on the Branch resource and plumbed to the
  reconciler. Spec-first: `api/openapi.yaml` `createBranch.at` + `Branch.bootstrap_at` + regenerated
  TS client.

### Tests
- Reconciler: `BuildBranchedCluster` bootstrap/`externalClusters` shape (origin at the parent's
  path, child keeps its own archive + compute), PITR `recoveryTarget`, a forked branch provisions
  via recovery bootstrap, and the root branch stays `initdb`. Store: `bootstrap_at` round-trips
  through create/get/reconcile-work; root branch has none. Server: `POST /branches {at}` (fork
  echoes `bootstrap_at`; `at` without `from_branch` → 400; malformed `at` → 400).

## [Phase 4 — read replicas & read endpoint] — 2026-07-19

### Added
- **`ro_pooled` read endpoint** — `POST /branches/{br}/endpoints {kind}` adds an endpoint to a
  branch. `ro_pooled` provisions a **read replica**: the reconciler scales the branch's cluster to
  a primary + hot-standby (`instances >= 2` whenever a read endpoint exists) and fronts the replicas
  with a dedicated read pooler (`BuildROPooler`, CNPG `type: ro`). `BackendFor(ro_pooled)` routes to
  that pooler. 409 if an endpoint of that kind already exists (branches ship with
  `rw_direct` + `rw_pooled`).
- **Non-disruptive endpoint reconcile** — adding an endpoint to a *ready* branch no longer needs a
  re-provision: `ListReconcileWork` now also returns ready branches with a `provisioning` endpoint,
  and a new reconcile path (`reconcileEndpoints` → `MarkEndpointsReady`) scales the cluster, builds
  the read pooler, and flips just the new endpoint to `ready` — the branch stays `ready` throughout.
- Spec-first: `api/openapi.yaml` `createEndpoint` + regenerated TS client. Teardown removes the read
  pooler alongside the rw pooler and cluster.

### Tests
- Store: `CreateEndpoint` (creates `ro_pooled` provisioning, dup-kind 409, missing-branch 404) and
  `MarkEndpointsReady`; reconciler picks up a ready branch with a pending read endpoint. Reconciler:
  a branch with a read endpoint provisions a 2-instance cluster + a `type: ro` pooler and marks the
  endpoint ready when healthy. Server: `POST /endpoints` (kind validation + scope + 409).

## [Phase 4 — suspend-on-idle] — 2026-07-19

Closes the automatic scale-to-zero loop: idle branches now hibernate on their own (the wake half
shipped in the three prior increments). ADR-015.

### Decided (ADR-015)
- The suspend decision is made by the **control plane** from gateway-reported activity **aggregated
  across all gateway replicas** — never by a single gateway, which sees only its own connections and
  would otherwise kill another replica's live connections. The sweep is **fail-safe**: it never
  suspends when no gateway is currently reporting, so reporting downtime can't mass-suspend the fleet.

### Added
- **Gateway activity reporting** — each gateway tracks per-branch active connection counts and
  periodically POSTs them to the control-plane's `POST /internal/gateway-activity` (same
  `NDB_GATEWAY_TOKEN` auth as wake). `PGGW_CONTROL_PLANE_URL`/`PGGW_GATEWAY_TOKEN` enable it;
  `PGGW_GATEWAY_ID` (hostname default) and `PGGW_ACTIVITY_INTERVAL` (default 15 s) tune it.
- **Control-plane aggregation + idle sweep** — a `branch_activity(branch_id, gateway_id,
  active_conns, reported_at)` telemetry table and `branches.last_active_at` (migration `0008`).
  `ReportGatewayActivity` upserts a gateway's counts and bumps `last_active_at` on observed activity;
  `MarkBranchReady`/`MarkBranchResumed` seed `last_active_at` so a freshly-ready branch gets a full
  grace period. `SweepIdleBranches` (run each reconcile pass) flips a ready branch to `suspending`
  only when its globally-summed active count is 0 AND it's been idle past `suspend_timeout_s` —
  and only while at least one gateway is recently reporting. `suspend_timeout_s = 0` disables
  autosuspend (paid-plan opt-out); the threshold lives entirely control-plane-side, so the gateway
  reports raw counts and needs no knowledge of it.

### Tests
- Store: `ReportGatewayActivity` aggregation across gateways, `last_active_at` seeding, and
  `SweepIdleBranches` (suspends a globally-idle branch past its timeout; does NOT suspend one with
  activity on another gateway, one within its grace period, one with `suspend_timeout_s = 0`, or any
  branch when no gateway is reporting). Server: the `/internal/gateway-activity` endpoint (auth +
  disabled without the token). Gateway: per-branch counting and the reporter loop.

## [Phase 4 — gateway hold-and-wake] — 2026-07-18

Scale-to-zero wake-on-connect goes live end to end (ADR-014): the pg-gateway now **holds** a
connection to a suspended endpoint, triggers a wake, and completes the connection once the branch
is ready — instead of rejecting it. This is the user-visible completion of the wake path built by
the two prior increments (the compute state machine + the internal wake endpoint).

### Added
- **`internal/wake`** — the wake trigger: a single authenticated POST to the control-plane's
  `POST /internal/branches/{br}/wake`, **coalesced per branch** via `singleflight` so a connection
  storm produces one wake, not one per connection (SECURITY_MODEL §2). The POST runs on its own
  bounded context, decoupled from any caller's cancellation, so one client giving up cannot abort
  the shared wake for the others.
- **Gateway hold-and-wake** (`proxy.holdAndWake`) — on a suspended endpoint the gateway triggers
  the wake, extends the connection deadline across the wake budget, and polls the route table until
  the endpoint reports `ready` (then proceeds to dial + forward + pipe) or the `WakeTimeout`
  (default 30 s, honest Gen-1 budget — ADR-004 p95 < 25 s) expires (clean `57P03` retry hint).
  Falls back to the pre-Phase-4 clean rejection when no waker is configured or a route has no
  `branch_id`.
- **Wake metrics** — `pggw_wakes_total{result}` (ready|timeout|error), `pggw_wake_wait_seconds`
  (histogram bucketed around the p50<10s/p95<25s target), `pggw_wake_holds_active` (gauge). The
  DEPLOYMENT §6 wake SLO is now directly measurable.
- **Config** — `PGGW_CONTROL_PLANE_URL` + `PGGW_GATEWAY_TOKEN` enable wake-on-connect (both
  required; wake is disabled otherwise); `PGGW_WAKE_TIMEOUT` tunes the hold budget. The proxy's
  route table is now behind a `RouteTable` interface for testability.

### Tests
- `wake`: request shape (method/path/bearer), non-2xx → error, per-branch coalescing (20
  concurrent → 1 POST; distinct branches not coalesced), caller-cancellation isolation — all under
  `-race`. `proxy`: `holdAndWake` proceeds-when-ready, times-out, rejects without a waker / without
  a `branch_id`, fails on wake error, and aborts on context cancel. Integration: a **real pgx
  client** connects through the gateway to a *suspended* endpoint, is held, woken, and completes
  `SELECT 1` against live Postgres once the route flips to ready.

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
