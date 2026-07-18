# Deployment Architecture — NimbusDB

**Status:** Draft v0.1

---

## 1. Substrate

**Decided (ADR-005, owner-approved 2026-07-17): managed Kubernetes + cloud object storage/KMS**
in `syd1` (ap-southeast-2) for launch. The architecture stays substrate-portable by
construction — everything above the Kubernetes API is identical across options — so the
bare-metal/Sydney-colo path (which aligns with Nimbus's own colo ambitions,
`hosting/docs/INFRASTRUCTURE.md`) remains available as a **later cell** rather than a migration
(MULTI_TENANCY §6). The concrete provider (EKS vs AKS vs GKE) is picked inside the Phase 1
Terraform module on quota/pricing at bootstrap time.

Requirements any substrate must meet: CSI snapshots + expansion, ≥ 2 AZs (or failure domains),
S3-compatible object storage in-region, KMS or HSM-equivalent, L4 load balancer for the gateway.

## 2. Environments

| Env | Purpose | Shape |
|---|---|---|
| `dev` (local) | day-to-day development | `kind` cluster via `tools/dev-up.sh`: CNPG, Cilium, Traefik, MinIO, control plane from source; seeded fixtures |
| `staging` | integration, chaos/restore drills, performance gates | scaled-down copy of prod (same manifests, smaller nodes); synthetic tenants + anonymized-shape workloads only (never customer data) |
| `prod syd1-a` | production cell | full HA layout below |

## 3. Production cluster layout (cell `syd1-a`)

```
node pools:
  system      (3 nodes)  — ingress, ArgoCD, cert-manager, observability, NATS
  controlplane(2–3 nodes)— control-plane API, reconcilers, console SSR, Temporal (P5+), control-plane PG (CNPG)
  gateway     (3 nodes)  — pg-gateway DaemonSet/Deployment behind one L4 LB (single stable IP + wildcard DNS)
  tenant-gp   (autoscaled)— general-purpose tenant branches (dev/preview density)
  tenant-prod (autoscaled)— production-tier branches; topology-spread across AZs
namespaces:
  platform-*  — one per platform component
  prj-<ulid>  — one per tenant project (MULTI_TENANCY)
```

DNS: `*.syd1.db.nimbus.app → gateway LB`; `api.db.nimbus.app`, `console.db.nimbus.app → Traefik`.

## 4. GitOps pipeline

```
PR → GitHub Actions (lint, typecheck, unit+integration tests, SAST, image build,
     trivy scan, cosign sign, push by digest)
   → merge to main
   → CD job bumps image digests in /deploy/k8s overlays (staging automatically)
   → ArgoCD syncs staging; smoke + e2e suite runs against staging
   → promotion PR (or tag) bumps prod overlay → ArgoCD syncs prod progressively
```

- **ArgoCD app-of-apps** owns everything in-cluster, including CNPG operator version and
  platform CRs. Manual `kubectl` changes are drift, alarmed and auto-reverted.
- **Terraform** owns everything below k8s (VPC/DNS/KMS/buckets/cluster/node pools), state in
  object storage with locking; applied via CI with manual approval on prod.
- **Secrets in git:** SOPS(age)-encrypted only; runtime secrets via External Secrets
  (SECURITY_MODEL §5).

## 5. Zero-downtime deploy rules

| Component | Strategy |
|---|---|
| control-plane API | Rolling; readiness-gated; DB migrations expand/contract (additive first, contract ≥ 1 release later) — same discipline both customer apps already use in CI |
| Reconcilers | Leader-elected; rolling; idempotent by contract so restarts mid-reconcile are safe |
| pg-gateway | Rolling with connection draining (stop accepting, drain existing up to 30 min, then terminate); LB health-check based ejection. Long-lived Postgres connections mean draining is the norm, and clients must tolerate reconnects (documented; poolers make this invisible to apps) |
| CNPG operator upgrades | Staged: staging soak ≥ 1 week → prod; operator upgrade does not restart tenant clusters (CNPG guarantee) — verified in staging each time |
| Tenant Postgres minor updates | CNPG rolling: replicas first, controlled switchover; HA tiers see ~1 reconnect blip; single-instance tiers get a maintenance-window restart with tenant notification |
| Console | Standard Next.js rolling deploy (stateless) |

## 6. Observability & operations

- **Metrics:** Prometheus (per-cell) + Thanos/remote-write for retention (evaluated Phase 7);
  Grafana dashboards versioned in `/deploy/k8s/observability`.
- **Logs:** Loki; **Traces:** OTel → Tempo (sampled). All services emit OTel from Phase 1.
- **Alerting:** Alertmanager → on-call (Phase 5+: paging); SLOs with error budgets:
  - API availability 99.9 %, gateway connect success 99.95 %,
  - wake p95 < 25 s (Gen 1) — measured directly by the gateway's `pggw_wake_wait_seconds`
    histogram, with `pggw_wakes_total{result}` and `pggw_wake_holds_active` for hold health,
  - provisioning median < 60 s / p95 < 90 s,
  - backup-verification job success = 100 % (any failure pages).
- **Runbooks** in `/docs/runbooks/`, one per alert, created with the alert (phase-gate item).
- **Status page** (Phase 7) fed by the same SLO probes.

## 7. Disaster recovery

- **Primitive:** object-storage backups + WAL archives (per branch) and control-plane PG's own
  PITR + a periodic logical dump shipped to a **second region/provider bucket** (guards against
  region/account-level loss).
- **Cell loss (RTO hours, RPO ≈ WAL-archive lag ≤ minutes):** Terraform re-creates the cell;
  ArgoCD re-syncs platform; reconcilers rebuild tenant clusters from `desired_state` via
  `recovery` bootstrap from object storage. The drill for this is executed on staging before
  external GA (Phase 7 exit criterion) and yearly after.
- **Control-plane DB loss:** restore from PITR; reconcilers reconcile observed drift; audit-log
  gap analysis procedure documented.
- **Backup-store loss:** the second-region copy is the answer; verification job alerts on
  replication staleness.

## 8. Cost & capacity posture

Dev/preview branch density (small CUs + scale-to-zero) is the primary cost lever; per-cell
capacity dashboards track: node headroom, PVC growth, WAL-archive egress, LB connection counts.
Capacity planning reviews are part of each phase-gate SRE review from Phase 4 on.
