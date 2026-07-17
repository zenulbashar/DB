# Changelog

All notable changes to this repository. Format loosely follows [Keep a Changelog](https://keepachangelog.com/); one entry per phase gate plus notable intermediate merges.

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
