---
title: Roles & databases
category: Connecting
order: 3
summary: Managing Postgres roles and databases on a branch, and the reveal-once password rules.
---

Each branch carries its own **roles** (Postgres login users) and **databases**.
They are branch-scoped: forking a branch copies them with the data, and changes
after the fork stay on their own branch.

## Roles

```bash
# list
GET  /branches/{br}/roles
# create — password returned exactly once
POST /branches/{br}/roles           {"name":"app_readonly"}
# rotate — new password returned exactly once
POST /branches/{br}/roles/{role}/reset-password
# remove
DELETE /branches/{br}/roles/{role}
```

Rules and behavior:

- Role names follow Postgres identifier rules: lowercase, start with a letter
  or `_`, max 63 chars (`^[a-z_][a-z0-9_]{0,62}$`).
- The password comes back **only** in the create/reset response. Store it in a
  secret manager immediately. There is no lookup API — by design.
- A role that owns a database can't be deleted (`409`) — drop or reassign the
  database first.
- Every project seeds `<project>_owner` on the default branch; treat it like a
  superuser for your app and create narrower roles for services that need less.

## Databases

```bash
GET    /branches/{br}/databases
POST   /branches/{br}/databases     {"name":"analytics","owner_role":"app_readonly"}
DELETE /branches/{br}/databases/{db}
```

- Same identifier rules as roles.
- Each database has an **owner role**, which has full rights on it.
- Multiple databases per branch are fine — they share the branch's compute.

## Suggested layout

For a typical app:

| Role | Used by | Rights |
|---|---|---|
| `<project>_owner` | migrations, admin | owner of the main database |
| `app` | the application | read/write via pooled endpoint |
| `app_readonly` | dashboards, analytics | read-only, ideally on `ro_pooled` |

Create the narrower roles with the API, then `GRANT` the exact privileges you
want using a direct-endpoint `psql` session as the owner role.
