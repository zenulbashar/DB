---
title: Importing an existing database
category: Imports & migration
order: 1
summary: Move a database from Neon, Supabase, RDS, Azure, or any Postgres — dump/restore or near-zero-downtime live sync.
---

The import engine moves an existing PostgreSQL database into a NimbusDB branch.
It supports two modes; pick by how much downtime you can afford.

| Mode | Downtime | Use when |
|---|---|---|
| `dump_restore` | a write-freeze for the copy duration | small DBs, dev/staging, tolerant apps |
| `logical_replication` | seconds (a cutover blip) | production databases |

Supported sources: `neon`, `supabase`, `rds`, `azure`, `generic` (any
reachable Postgres). The adapters differ only in connection/permission advice —
it's the same engine underneath.

## Starting an import

```bash
POST /projects/{prj}/imports
{
  "source_kind": "neon",
  "mode": "logical_replication",
  "source_url": "postgres://user:pass@source-host/db"
}
```

The `source_url` is **write-only**: it is encrypted at rest and never returned
by any read path. Rotate the source credential after the migration anyway.

## The state machine

An import advances through explicit states you can watch
(`GET /imports/{imp}`):

```
pending → preflight → schema_copy → initial_copy → live_sync
        → cutover_ready → cut_over → verified
```

(`failed` / `aborted` are terminal exits; abort any time before cutover with
`POST /imports/{imp}/abort`.)

- **preflight** inspects the source — version, extensions, collations, size,
  and session-state usage that affects endpoint choice (see *Endpoints*) — and
  produces a report on the import record.
- **live_sync** (logical replication mode) streams changes continuously; lag
  is tracked in the import's checkpoints.
- **cutover_ready** is a *human gate*: the platform will not cut over on its
  own.

## Cutover — the human gate

When you're ready (low-traffic window, lag ≈ 0):

1. Freeze writes on the source (or your app).
2. `POST /imports/{imp}/cutover` — the engine syncs sequences, verifies row
   counts/checksums, then completes: **verification runs before** the
   replication link is torn down, so a failed check leaves the source intact
   as your rollback.
3. Point your application's `DATABASE_URL` at the NimbusDB endpoint.

Keep the source alive until you're satisfied — that's your instant rollback.

## After the import

- Recreate roles/passwords on the branch (passwords don't migrate).
- Check the preflight report's endpoint advice — apps using LISTEN/NOTIFY or
  advisory locks need the **direct** endpoint for those connections.
- Run `ANALYZE` after large imports for accurate query plans.
