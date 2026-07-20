---
title: Using the console
category: Getting started
order: 2
summary: A tour of the web console — connect, dashboard, project pages, and what each control does.
---

The console is the web UI over the same API your automation uses — everything
it does, you can also do with `curl` and an API key.

## Connect

Paste an `ndb_` API key on the **Connect** screen. The console validates it
against the platform before storing it in an **httpOnly cookie** (30 days,
never readable by page scripts). **Sign out** (top right) clears it. The
console never displays your key back, and it never sees role passwords except
in the one-time reveal moments described below.

## Projects dashboard

Your projects, each with its lifecycle dot, Postgres version, and region.
**New project** walks through org / name / region / PG version and then shows
the seeded owner credentials **exactly once** — copy all three fields
(database, role, password) before leaving the page.

## Project page

- **Connect panel** — the assembled connection string for the default
  endpoint, password masked. Copy it and substitute your stored password.
- **Branches** — each branch card shows its state dot, role badge, endpoints
  (with copy buttons), and CU range.
  - **Suspend / Resume** appear when the branch state allows them.
  - **Resize** opens an inline CU input clamped to the branch's bounds.
  - Transitional states show as *working…* and settle on their own.
- **New branch** — name, role, and the parent to fork from.

## State dots

| Color | Meaning |
|---|---|
| blue (pulsing) | provisioning / resuming / resizing — converging |
| green | ready |
| grey | suspended |
| amber (pulsing) | deleting |
| red | error — see *Troubleshooting* |

## What's not in the console (yet)

Role/database management, API-key management, the audit-log viewer, metrics,
and the SQL editor are API-first today and arrive in the console next — the
relevant KB articles show the API calls for each.
