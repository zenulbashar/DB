# NimbusDB — Serverless PostgreSQL Platform

A multi-tenant managed/serverless PostgreSQL platform (Neon-class) that integrates with the
[Nimbus hosting platform](https://github.com/zenulbashar/hosting), built to first migrate
**Prompt2Eat** and **Roster** off Neon, then onboard external tenants.

> **Status: Phase 0 — planning.** No production code exists yet, by design.
> Implementation begins only after the architecture plan below is approved.

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

## Rules of the repo

1. **Docs first.** Implementation that diverges from `docs/architecture` updates the docs in the
   same PR before review.
2. **Spec-first API.** `api/openapi.yaml` precedes handlers; clients are generated.
3. Every phase ends with tests, self/architecture/security/performance review, a
   [CHANGELOG](CHANGELOG.md) entry, and a commit — see MASTER plan §6.
