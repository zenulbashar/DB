# Risk Register — NimbusDB

**Status:** Living document; reviewed at every phase gate. Severity = Impact × Likelihood (H/M/L).

| ID | Risk | Sev | Phase(s) | Mitigation / treatment | Trigger to re-assess |
|---|---|---|---|---|---|
| **R-1** | **Scope ambition vs team size.** The feature list spans what Neon+Supabase built with large teams; a single-operator team can stall or ship shallow versions of everything. | **H** | all | Strict phase gates; "boring data path" rule; Gen-1/Gen-2 split (ADR-004); Phase 5 (first-customer proof) prioritized over breadth; roadmap modules deferred by policy. | Any phase slipping into two adjacent phases' scope. |
| **R-2** | **Data loss on tenant databases** (bad backup, un-replayable WAL, silent corruption). Existential — this product's only non-negotiable. | **H** | 2+ | Continuous WAL archive + nightly base backups; **automated nightly restore-verification with paging on failure**; pre-destructive-op backups; second-region backup copies; restore drills as phase-gate items; synchronous replication on HA tier. | Any verification failure; any storage-class change. |
| **R-3** | **Scale-to-zero cold starts disappoint** (Gen 1 seconds-scale vs Neon's sub-second), hurting the serverless positioning. | M | 4, 8 | Honest documented targets (p95 < 25 s); suspend disabled by default on production tier; Gen 2 evaluation (Neon OSS storage) scheduled; wake-coalescing + prewarm hooks (deploy-time wake ping from Nimbus integration). | Tenant complaints; Gen-2 evaluation results. |
| **R-4** | **Pooler semantics break tenant apps** (transaction-mode vs session state: pg-boss, advisory locks, LISTEN/NOTIFY, prepared statements). Already observed as a live constraint in both first customers. | M | 2, 5 | Dual-endpoint model is a launch requirement; PgBouncer `max_prepared_statements` enabled; migration preflight detects session-state usage patterns and prescribes endpoint mapping; docs page "which endpoint do I use". | Any support incident traced to pooling. |
| **R-5** | **Kubernetes + CNPG operational complexity** exceeds operator capacity (upgrades, CSI quirks, CNI issues). | M→H | 2+ | Managed-k8s-first substrate preference (ADR-005); staging soak for every operator/k8s upgrade; runbook-per-alert discipline; cell model bounds blast radius; minimal moving parts until Phase 5 (no Temporal/ClickHouse before needed). | First prod incident caused by platform-layer upgrade. |
| **R-6** | **First-customer migration causes visible downtime/data loss** (Prompt2Eat takes live payments via Stripe webhooks; Roster's auth sessions live in the DB). | H | 5 | Logical-replication live-sync with checksum verification; rehearsal cutovers on branches (Phase 2/4 acceptance items); write-freeze windows chosen from traffic data; instant rollback = keep Neon as source until verification passes; runbooks in MIGRATION_STRATEGY §6–7. | Rehearsal metrics missing targets. |
| **R-7** | **Custom pg-gateway defects** (protocol edge cases, TLS/SNI quirks, connection leaks) — it fronts every tenant byte. | M | 2+ | Deliberately tiny scope (route, hold, count — no protocol rewriting); soak tests with real drivers (pg, psql, Drizzle, Prisma, pgAdmin); fuzz the startup-packet parser; canary deploys; per-connection metrics from day one. | Any gateway-attributed connection failure class. |
| **R-8** | **Secrets handling bug** (connection strings are the crown jewels; Nimbus currently stores env vars plaintext — integration must not inherit that). | H | 2, 6 | Envelope encryption from Phase 2 (never plaintext at rest); reveal-once API semantics; audited reveals; Nimbus integration passes secrets by reference until Nimbus adds encrypted env storage (raised as integration requirement, Phase 6). | Any secret found in logs/backups. |
| **R-9** | **Noisy neighbours degrade paid tenants** on shared nodes. | M | 4+ | Guaranteed-QoS pods for production tier; IOPS budgeting; per-endpoint caps; density only on dev tier; cells for big tenants. | p95 latency SLO burn on production tier. |
| **R-10** | **Cost overrun of the platform substrate** (HA control plane + observability + per-branch clusters is heavy at low tenant counts). | M | 2–7 | Substrate decision weighs cost (ADR-005); scale-to-zero density on dev branches; capacity reviews each gate; MinIO/self-host options preserved by S3-compat abstraction. | Monthly infra cost > plan at gate review. |
| **R-11** | **Nimbus is a prototype with a simulated data plane** — "deploy compute next to your DB" delivers simulated deployments until Nimbus's machine layer ships; integration timing is outside this repo's control. | M | 6 | Loose coupling by contract (soft links, webhooks) so NimbusDB never blocks on Nimbus; Phase 6 scoped to the *contract* + real control-plane calls (which work today); product copy must not oversell until Nimbus data plane is live. | Nimbus data-plane milestone dates. |
| **R-12** | **Single-region concentration** (`syd1`): a regional/provider incident takes the whole platform down. | M | 2–8 | Second-region backup copies from Phase 2 (DR primitive); documented cell-rebuild drill; multi-region design constraints enforced now (region in every API/placement decision) so Phase 8 is expansion, not rework. | External-tenant GA (raises stakes). |
| **R-13** | **Neon OSS adoption (Gen 2) proves heavier than its benefit** — pageserver/safekeeper ops burden, upstream drift. | M | 8 | It's an *evaluation* with written exit criteria, not a commitment; Gen 1 remains fully supported; API designed storage-agnostic. | Evaluation report. |
| **R-14** | **Compliance debt discovered late** (SOC2 evidence gaps, AU privacy obligations for external tenants). | L→M | 7 | Controls designed-in (SECURITY_MODEL §8); evidence generation automated from Phase 2 (audit log, CI attestations); legal/DPA work explicitly scheduled Phase 7. | First enterprise-prospect questionnaire. |
| **R-15** | **Design export arrives late and forces console rework.** | L | 3 | Token-isolated theming (DESIGN_SYSTEM_MAPPING §5); re-skin contained by construction. | Export arrival. |

**Retired risks:** none yet.

**Review log:**
- 2026-07-17 — initial register (Phase 0).
- 2026-07-18 — **adversarial code audit** (8 dimensions, each finding independently
  verified) over the ~7.4k-line implementation. 19 confirmed defects found and fixed the
  same day, notably: idempotency cache persisted plaintext credentials at rest (now
  envelope-encrypted) and admitted racing duplicate POSTs (now serialized per key) — both
  strengthen R-8; reconciler branch-teardown wedged forever on a foreign-key violation once
  an import referenced the branch (FKs now `ON DELETE SET NULL`, orphaned secrets cleaned) —
  R-5; tenant NetworkPolicies blocked CNPG replication/operator/metrics and left egress
  wide open (ingress/egress allow-lists corrected) — R-9/R-5; the migration parity check used
  a bounded sample that could miss single-row corruption in large tables (now full-table by
  default) — directly strengthens R-2/R-6; logical-replication cutover tore down the link
  before verifying (reordered: verify-then-cutover) and could leak a WAL-retaining slot on an
  unreachable source (safe detach-then-drop ordering) — R-6. Regression tests added for each
  security- and durability-critical fix. This is the phase-gate self-review discipline
  (MASTER §6) applied retroactively across Phases 1–2 + the migration engine.
- 2026-07-18 — **import-worker hardening audit** (adversarial, each finding refute-verified)
  over the now-runnable worker + migration runner. 11 confirmed defects (2 critical) fixed
  the same day with regression tests. Two directly strengthen R-6: a *concurrent double-drive*
  — the `FOR UPDATE SKIP LOCKED` claim released its lock the moment the row was read, so two
  replicas could drive the same in-flight migration in parallel (now an atomic lease with
  `claimed_by`/`claimed_at` + TTL takeover, migration `0006`); and a runner
  `live_sync → live_sync` self-transition that failed *every* logical migration on its first
  lag check (the state machine has no self-edges; the runner now holds state on a legal wait).
  A failed logical migration could leak a WAL-retaining slot on the source when the target was
  unreachable — `cleanupOnFailure` now drops the source slot independently (R-2/R-6). Two
  strengthen R-8: `pg_dump`/`pg_restore` passwords moved out of `argv` into `PGPASSWORD`, and
  `urlToConnInfo` now quotes/escapes every conninfo value (no keyword injection) and redacts
  the URL from parse errors. Plus a per-poll target-connection leak (R-7) and a
  target-owner-role mismatch (wrong-role connect) fixed. No new open risks; existing
  mitigations tightened.
- 2026-07-18 — **Phase 4 scale-to-zero spine** (control-plane suspend/resume state machine +
  reconciler CNPG hibernation + route mapping; gateway hold-and-wake and idle-suspend follow).
  ADR-014 resolves a pre-existing doc inconsistency on the wake-trigger transport (the §2 mermaid
  drew a forbidden direct gateway→reconciler RPC): wake/suspend are **desired-state flips** the
  reconciler converges, and the gateway's on-connect wake is a single **coalesced** authenticated
  POST to the control-plane API — a bounded, reviewed expansion of the gateway's "route, hold,
  count" scope (**R-7**), deliberately *not* DB access or a full API client. This makes the wake
  path `gateway → API → control-plane DB → reconciler` explicit as the highest-availability tier
  (**R-3**: suspended branches cannot wake during a full control-plane outage — honest, documented).
  Wake coalescing (one wake per branch under a connection storm) is preserved from SECURITY_MODEL
  §2; the idle-suspend and billing-suspend paths reuse one reconciler action. No new open risks.
