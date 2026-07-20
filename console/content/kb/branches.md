---
title: Branches & data forks
category: Projects & branches
order: 2
summary: Branches are full copies of your database — including point-in-time forks — with their own compute and endpoints.
---

A **branch** is a complete, independent Postgres instance forked from another
branch's data. Use branches the way you use git branches: previews, migration
rehearsals, load tests, per-developer databases — all without touching
production.

## How a fork works

Every non-root branch is a **data fork**: the platform restores the parent's
continuous backup (WAL archive) into a fresh cluster. The fork contains
everything — schema, data, roles, extensions — as of the fork point. Changes on
the branch never affect the parent, and vice versa.

- **Fork from latest:** omit `at` and you get the parent's most recent state.
- **Point-in-time fork:** pass `at` (RFC3339 timestamp) to fork the parent *as
  it was at that moment* — invaluable for "what did the data look like before
  the bad deploy?" investigations. `at` must fall inside the parent's backup
  retention window (see *Backups & PITR*).

```bash
curl -X POST $NDB_API/v1/projects/$PRJ/branches \
  -H "Authorization: Bearer $NDB_KEY" -H 'Content-Type: application/json' \
  -d '{"name":"preview-42","from_branch":"br_…","at":"2026-07-18T09:00:00Z","role":"preview"}'
```

In the console, use the **New branch** form on the project page.

## Branch roles

`production`, `preview`, `development` — the role is a label that drives
defaults and safety rails (production branches get HA compute; previews are
cheap and auto-suspend aggressively). It does not change what the branch *is*:
every branch is a real Postgres.

## Lifecycle & states

`provisioning → ready`, then the compute states `suspending / suspended /
resuming` (see *Scale to zero*) and `resizing` (see *Compute sizing*). A fork
that cannot restore (e.g. `at` outside the retention window) lands in `error` —
delete it and re-create with a valid target.

Expect a fork to take seconds to minutes depending on data size: it is a real
restore, not a metadata trick.

## Good practices

- Keep `main` as the production branch and fork everything else from it.
- Name branches after their purpose (`preview-pr-42`, `loadtest-oct`).
- Delete merged/stale branches — each ready branch holds real compute and
  storage. Suspended branches cost storage only.
- The default branch is delete-protected; every other branch can be deleted
  freely (`DELETE /branches/{br}`).
