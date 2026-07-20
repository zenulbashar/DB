---
title: Projects
category: Projects & branches
order: 1
summary: What a project is, what gets seeded when you create one, and project lifecycle.
---

A **project** is the top-level container for one application's databases. It
belongs to an organization, lives in one region, and pins a Postgres major
version. Everything inside a project — branches, endpoints, roles, databases,
imports — inherits that placement.

## What you get on creation

Creating a project seeds a working stack:

| Item | Value |
|---|---|
| Default branch | `main`, role `production` |
| Compute bounds | 0.25–2 CU (adjustable per branch) |
| Owner role | `<project>_owner` — password shown **exactly once** |
| Database | named after the project, owned by the owner role |
| Endpoints | `rw_pooled` (connect here by default) and `rw_direct` |

The project starts in state `pending` and moves to `ready` once the platform's
reconciler has provisioned the underlying cluster. You can already browse it in
the console while it provisions.

## Settings

- **Name** can be changed any time (`PATCH /projects/{prj}`); the slug and IDs
  are stable.
- **Region** and **Postgres version** are fixed at creation. To move a project
  across regions or major versions, create a new project and use an *import*
  (logical replication gives you a near-zero-downtime move).

## Deleting

`DELETE /projects/{prj}` is a soft delete that tears down compute and schedules
storage cleanup per the retention policy. The default branch is delete-protected
individually — deleting the project is the way to remove it.

## Limits & states

Project states: `pending → provisioning → ready`, with `error` and `deleting`
as exceptional states. If a project sits in `error`, check the branch states on
its detail page first — a branch-level problem (e.g. a failed fork) is the most
common cause — then contact your operator with the project ID.
