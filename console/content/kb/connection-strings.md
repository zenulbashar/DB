---
title: Connection strings & secrets
category: Connecting
order: 2
summary: Where to find your connection string, why it's masked, and how the audited reveal works.
---

A connection string looks like:

```
postgresql://<role>:<password>@ep-….syd1.db.nimbus.app/<database>?sslmode=require
```

The console's **Connect** panel and `GET /projects/{prj}/connection-uri`
assemble it for you from a branch, endpoint kind, role, and database.

## Why the password shows as `****`

NimbusDB treats passwords as **reveal-once** secrets:

- A role's password is returned **exactly once** — when the role is created or
  its password is reset. It is stored encrypted and never re-displayed by
  default.
- Connection strings on read paths are therefore **masked**
  (`role:****@…`). Copy the masked string and substitute the password you
  stored, or use the reveal below.

## The audited reveal

Callers whose API key has the `roles:write` scope may request the full string:

```
GET /projects/{prj}/connection-uri?reveal=true
```

Every reveal is written to your organization's **audit log** (who, when, which
role) — so secret access is always attributable. If you can't justify a reveal
being on the record, reset the password instead.

## Choosing what the string points at

Query parameters on `connection-uri` let you pick exactly what you need:

| Param | Default | Notes |
|---|---|---|
| `branch` | project default branch | any branch ID |
| `endpoint` | `rw_pooled` | `rw_pooled`, `rw_direct`, `ro_pooled` |
| `role` | project owner role | any role on the branch |
| `database` | project database | any database on the branch |

## Rotation

Reset a role's password with
`POST /branches/{br}/roles/{role}/reset-password` — the new password is
returned once. Update your app's secret store and redeploy; the old password
stops working immediately. Rotate on any suspicion of exposure and on staff
departure.

## TLS

Always keep `sslmode=require` (or stricter). Endpoints only accept TLS.
