# NimbusDB — Serverless PostgreSQL Platform

A multi-tenant managed/serverless PostgreSQL platform (Neon-class) that integrates with the
[Nimbus hosting platform](https://github.com/zenulbashar/hosting), built to first migrate
**Prompt2Eat** and **Roster** off Neon, then onboard external tenants.

> **Status: Phases 1–4 implemented; Phase 3 console in progress.** The control-plane API,
> pg-gateway, reconciler/CNPG provisioning, envelope secrets, backup/recovery, the import engine,
> and the elastic-compute mechanisms (scale-to-zero wake/suspend, read replicas, branching, vertical
> resize) are built and tested. The console reads live control-plane data and can connect, create
> projects, and manage branches (`make smoke` verifies this end-to-end). Phases 5–8 (Temporal-driven
> imports at scale, Nimbus integration, billing, multi-region) are gated on external systems. See the
> [CHANGELOG](CHANGELOG.md) and [ROADMAP](docs/architecture/ROADMAP.md) for exact state.

## Architecture documentation (source of truth)

Start here: [`docs/architecture/MASTER_IMPLEMENTATION_PLAN.md`](docs/architecture/MASTER_IMPLEMENTATION_PLAN.md)

| Document | Purpose |
|---|---|
| [MASTER_IMPLEMENTATION_PLAN](docs/architecture/MASTER_IMPLEMENTATION_PLAN.md) | Mission, inputs analysed, phase plan, working agreements |
| [SYSTEM_ARCHITECTURE](docs/architecture/SYSTEM_ARCHITECTURE.md) | Components, flows, technology justification |
| [DATABASE_ARCHITECTURE](docs/architecture/DATABASE_ARCHITECTURE.md) | Tenant Postgres, PITR, branching, pooling; control-plane schema |
| [MULTI_TENANCY](docs/architecture/MULTI_TENANCY.md) | Tenancy hierarchy and isolation layers |
| [ROADMAP](docs/architecture/ROADMAP.md) | Phase scopes and acceptance criteria |
| [API_SPECIFICATION](docs/architecture/API_SPECIFICATION.md) | REST v1 resource model and conventions |
| [SECURITY_MODEL](docs/architecture/SECURITY_MODEL.md) | Identity, secrets, encryption, audit, SOC2 mapping |
| [DEPLOYMENT_ARCHITECTURE](docs/architecture/DEPLOYMENT_ARCHITECTURE.md) | Environments, GitOps, DR |
| [DESIGN_SYSTEM_MAPPING](docs/architecture/DESIGN_SYSTEM_MAPPING.md) | Console design language and token layer |
| [RISK_REGISTER](docs/architecture/RISK_REGISTER.md) | Ranked risks and mitigations |
| [MIGRATION_STRATEGY](docs/architecture/MIGRATION_STRATEGY.md) | Import engine + Roster/Prompt2Eat cutover runbooks |
| [DECISION_LOG](docs/architecture/DECISION_LOG.md) | ADRs and open questions for the owner |

## Local development

Prerequisites: Go, Node, and a Postgres for the control plane (docker-compose provides one with the
non-superuser `ndb_app` role that RLS requires).

```sh
make dev-db        # start local control-plane Postgres (docker compose, :5433)
make dev           # run the control-plane API (auto-migrates) against it
make console-dev   # run the Next.js console (expects the API at NDB_API_URL, default :8080/v1)
make test          # unit tests across all Go modules (no external deps)
make smoke         # end-to-end: bootstrap + create a project via the API, then assert the
                   # console renders that live data. Needs a FRESH DATABASE_URL, e.g.:
                   #   DATABASE_URL=postgres://ndb_app:ndb_app@localhost:5433/nimbusdb_cp?sslmode=disable make smoke
```

The data plane (per-tenant Postgres clusters) needs a `kind` cluster with CloudNativePG — see
`tools/dev-up.sh`. The control plane, console, and import engine run without it.

**Deploying for real:** the self-host profile (ADR-020) runs the whole platform on one VM with
k3s + CNPG + MinIO — see [`docs/deploy/AZURE_VM.md`](docs/deploy/AZURE_VM.md) (setup) and
[`docs/deploy/BINARYLANE_MIGRATION.md`](docs/deploy/BINARYLANE_MIGRATION.md) (moving providers).
Images build to GHCR on every merge to `main` (`.github/workflows/release.yml`).

The console serves two shells: the tenant console at `/` (sign in with an `ndb_` API key; in-app
help at `/kb`) and the **operator console** at `/admin` (sign in with `NDB_ADMIN_TOKEN` —
`make dev` exports `dev-admin-token`; see ADR-018).

## Rules of the repo

1. **Docs first.** Implementation that diverges from `docs/architecture` updates the docs in the
   same PR before review.
2. **Spec-first API.** `api/openapi.yaml` precedes handlers; clients are generated.
3. Every phase ends with tests, self/architecture/security/performance review, a
   [CHANGELOG](CHANGELOG.md) entry, and a commit — see MASTER plan §6.
