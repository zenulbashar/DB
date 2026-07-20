---
title: Read replicas
category: Compute & scaling
order: 3
summary: Add a read-only endpoint backed by a hot standby to scale reads and isolate heavy queries.
---

A **read replica** gives a branch a second, read-only lane: a hot standby of
the primary fronted by its own pooled endpoint (`ro_pooled`). Use it to move
read-heavy or spiky workloads — dashboards, reports, exports, search
indexing — off the primary.

## Adding one

```bash
POST /branches/{br}/endpoints   {"kind":"ro_pooled"}
```

The endpoint starts `provisioning` while the platform scales the branch to a
primary + standby and brings up the read pooler; it flips to `ready` when the
replica is streaming. Your existing read/write endpoints are untouched
throughout — adding a replica is non-disruptive.

One `ro_pooled` endpoint per branch; a duplicate request returns `409`.

## Connecting

Point read-only clients at the `ro_pooled` host (visible on the branch card),
with a **read-only role** for defense in depth (see *Roles & databases*). The
replica rejects writes at the Postgres level regardless.

## What to expect

- **Replication lag** is normally sub-second but is not zero. Don't read your
  own just-committed write from the replica; keep read-after-write flows on
  the primary.
- The replica shares the branch's CU sizing — a branch with a read endpoint
  runs two instances of `current_cu` each.
- **HA for free:** the standby doubles as a failover target; if the primary
  fails, the platform promotes the standby.
- Long analytical queries can still be cancelled by replication conflicts;
  for heavy analytics with no freshness requirement, a **fork** (see
  *Branches & data forks*) is often the better tool — it's fully isolated.

## Removing

Delete the endpoint to drop back to a single instance:
`DELETE` is not yet exposed for endpoints in v1 — scale the branch's role in
the meantime or contact your operator. (Tracked on the roadmap.)
