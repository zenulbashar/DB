# Security Model — NimbusDB

**Status:** Draft v0.1 · SOC2-ready by design; certification work is Phase 7.

---

## 1. Principles

1. **Least privilege everywhere** — humans, services, tenants, and CI each get the minimum
   scope that lets them function, time-bounded where possible.
2. **Tenant data is radioactive** — it never appears in logs, metrics, traces, error reports,
   or support tooling. Query insights store normalized shapes only.
3. **Every privileged action is attributable** — append-only audit log with actor, action,
   target, origin.
4. **Assume control-plane compromise is the worst case** — encryption and isolation are layered
   so that a control-plane API bug does not directly yield tenant data (the API holds
   references and ciphertext, not plaintext credentials at rest).

## 2. Threat model (summary)

| Threat | Primary controls |
|---|---|
| Tenant → tenant lateral movement | Per-branch clusters, namespaces, Cilium default-deny, per-project storage credentials (MULTI_TENANCY §2) |
| Tenant → platform escalation | No superuser for tenants; extension allowlist; non-root pods; seccomp; no hostPath; egress-deny from tenant pods |
| Stolen API key | Scoped keys, hashed at rest, revocation, last-used tracking, rate limits, anomaly alerts (Phase 7), IP allowlists per key (Phase 7) |
| Credential leak in tenant app (e.g. leaked `DATABASE_URL`) | Per-role rotation API without downtime (dual-password rotation window), connection audit metrics, TLS-only endpoints |
| Insider / operator abuse | Admin portal on separate authn (SSO+MFA), separate hostname; break-glass access is JIT-granted, logged, and alerts the org owner (Phase 7); no standing psql access to tenant clusters |
| Supply chain | Pinned digests for all images, SBOM + vulnerability scanning in CI, signed images (cosign) enforced by admission policy, Renovate-managed updates |
| Data-plane node compromise | Encryption at rest, per-namespace workload identity for object storage, short-lived certs, node isolation for cells |
| Backup exfiltration | Backups encrypted at rest; bucket policies scoped per project prefix; access logged; restore requires control-plane authz |
| DoS on gateway/API | L4 LB + gateway connection caps per endpoint, API rate limits, wake-request coalescing (one wake per branch regardless of connection storm) |

## 3. Identity & authentication

- **Console users:** email magic-link at minimum (parity with both customer apps' Auth.js
  usage), OIDC (Google/GitHub) next; MFA (TOTP/WebAuthn) required for org `owner`/`admin` from
  Phase 7. Sessions: httpOnly, SameSite=lax, 30-day server-side sessions, revocable.
- **API keys:** `ndb_` prefix + 256-bit random; SHA-256 hash stored; scopes required at creation;
  shown once. (Deliberately the same shape as Nimbus's `nbt_` tokens — one mental model.)
- **Service-to-service (internal):** mTLS via cluster PKI (cert-manager); SPIFFE-style
  identities per service; no shared static internal secrets.
- **Platform operators:** SSO + MFA; role-based (admin portal roles: support-read,
  operator, security); production kubectl access via short-lived certs through an audited
  bastion flow, alarmed by default.

## 4. Authorization

- Org-scoped RBAC (roles in MULTI_TENANCY §4) enforced in the API layer; repository layer
  injects `org_id`; Postgres RLS as second net on the control-plane DB.
- Tenant Postgres: tenants own their roles inside their branch only; role creation flows through
  the API so grants stay auditable; no `SUPERUSER`, `pg_execute_server_program`,
  `pg_read/write_server_files` ever granted.
- Admin API physically separated (hostname + network policy + separate authn) from tenant API.

## 5. Secrets & encryption

| Class | Handling |
|---|---|
| Tenant DB passwords, webhook secrets, Nimbus integration tokens | **Envelope encryption**: AES-256-GCM data keys per secret, wrapped by a root KEK in cloud KMS (or HSM-backed equivalent; self-hosted fallback: age key in an isolated KMS service). Ciphertext in `secrets` table with `key_version` for rotation. Plaintext exists only in request-scoped memory. |
| Platform component secrets (control-plane DB creds, NATS creds) | Kubernetes Secrets sourced via External Secrets Operator from the KMS-backed store; **SOPS(age)-encrypted** in git where GitOps requires them. Never plaintext in git. |
| TLS | cert-manager: public certs via ACME (Let's Encrypt) for `*.{region}.db.nimbus.app` (wildcard, DNS-01); internal mTLS via private CA. Postgres endpoints TLS-required (`sslmode=require` minimum; `verify-full` documented and supported). |
| At rest | Storage-class encryption for PVCs; SSE for object storage; control-plane DB on encrypted volumes. Backups additionally client-side encrypted (Barman Cloud AES) from Phase 2. |
| In transit | TLS 1.2+ everywhere external; mTLS internal; no plaintext Postgres listeners. |

**Rotation:** KEK rotation re-wraps data keys (lazy); tenant password rotation is a first-class
API with a dual-validity window so apps can roll without downtime; platform cert rotation is
cert-manager-automated; API keys support expiry.

## 6. Audit & logging

- `audit_log` (control plane): append-only (no UPDATE/DELETE grants; retention ≥ 1 year),
  covering all mutations, credential reveals, SQL-editor executions, admin-portal actions,
  break-glass events. Tenant-visible via `GET /orgs/{org}/audit-log`.
- Platform logs (Loki): structured, tenant-data-free by policy + scrubbing middleware; 30–90 day
  retention by stream.
- Postgres logs for tenant branches: available to the owning tenant (console log viewer,
  Phase 7); platform staff access requires break-glass.

## 7. Data lifecycle & residency

- Region-pinned data: tenant bytes (data, WAL, backups, metrics) never leave the project's
  region (`syd1` at launch — relevant for the AU customer base).
- Deletion: MULTI_TENANCY §5 timelines; crypto-shredding option (delete wrapped keys) for
  regulated tenants (Phase 8).
- DR: see DEPLOYMENT_ARCHITECTURE §7 (backups are the DR primitive; restore drills mandatory).

## 8. SOC2 readiness mapping (build-time, certify Phase 7)

| Trust criterion | Architectural support |
|---|---|
| Security | RBAC, MFA, network segmentation, vuln management, signed supply chain |
| Availability | HA tiers, chaos drills, restore verification, incident runbooks, status page (Phase 7) |
| Confidentiality | Envelope encryption, tenant-data-free telemetry, access reviews (quarterly, tooled via admin portal) |
| Processing integrity | Reconciler idempotency, migration checksums, backup verification jobs |
| Privacy | Region pinning, deletion SLAs, audit trail, DPA templates (Phase 7 legal work) |

Evidence generation is designed-in: audit log, CI attestations, access-review exports — so the
Phase 7 audit is collection, not construction.

## 9. Secure development practice

- CI: SAST (golangci-lint + gosec, eslint security), dependency audit, secret scanning
  (gitleaks) on every PR; images scanned (trivy) and signed (cosign); IaC scanned (tfsec/checkov).
- Security review is a named phase-gate lens (MASTER plan §6) — each phase's PR carries a
  written security self-review.
- External penetration test before external-tenant GA (Phase 7 exit).
- Vulnerability disclosure policy + security.txt at GA.
