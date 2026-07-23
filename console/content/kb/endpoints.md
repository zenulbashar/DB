---
title: Endpoints — which one do I use?
category: Connecting
order: 1
summary: Pooled vs direct vs read-only endpoints, and the session-state caveats that decide which your app needs.
---

Every branch exposes up to three **endpoints** — stable hostnames like
`ep-….syd1.db.zaleit.com.au` that survive suspends, resizes, and failovers.

| Kind | Name in console | Use it for |
|---|---|---|
| `rw_pooled` | Pooled (read/write) | **Default.** Application traffic — APIs, web apps, serverless functions |
| `rw_direct` | Direct (read/write) | Migrations, admin tools, and anything needing session state |
| `ro_pooled` | Pooled (read-only) | Read scaling against replicas (see *Read replicas*) |

## The rule of thumb

> **Apps connect pooled. Humans and migrations connect direct.**

The pooled endpoint multiplexes thousands of client connections onto a few
Postgres backends (transaction-mode pooling). That's what makes serverless
workloads viable — but it means **session state does not survive between
transactions**.

Use the **direct** endpoint when you rely on any of these:

- `LISTEN`/`NOTIFY` (e.g. pg-boss, Graphile Worker queues)
- Session-level advisory locks (`pg_advisory_lock`)
- `SET`/`SET SESSION` settings you expect to persist
- Long-lived cursors (`DECLARE … WITHOUT HOLD`) across transactions
- Schema migrations (Prisma Migrate, Drizzle Kit, Flyway — point the
  *migration* URL at direct, the *app* URL at pooled)

Prepared statements **are** supported through the pooler
(`max_prepared_statements` is enabled), so standard drivers like `pg`, Prisma
and Drizzle work pooled out of the box.

## Adding endpoints

Branches ship with `rw_direct` + `rw_pooled`. Add a read-only endpoint with
`POST /branches/{br}/endpoints {"kind":"ro_pooled"}` — this provisions a read
replica behind it. One endpoint of each kind per branch; a duplicate returns
`409`.

## Endpoint states

Endpoints track their branch: `provisioning → ready`, and `suspended` while the
branch's compute is scaled to zero. A connection to a suspended endpoint is
**held while the branch wakes** — not refused (see *Scale to zero*).
