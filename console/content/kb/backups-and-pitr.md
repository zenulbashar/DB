---
title: Backups & point-in-time recovery
category: Data safety
order: 1
summary: Continuous WAL archiving, retention windows, and how to actually recover data.
---

Every branch is continuously protected: a base backup plus **WAL archiving**
means the platform can reconstruct the branch *as it was at any moment* inside
its retention window. There is nothing to schedule and no backup button to
remember.

## Retention

`retention_days` is per-branch (default **7**, range 1–30):

```bash
PATCH /branches/{br}   {"retention_days": 14}
```

Longer retention = a wider recovery window = more archive storage. Production
branches usually deserve 14–30 days; previews are fine at the minimum.

## Recovering data — the fork-first workflow

The safest recovery is **not** an in-place restore. It's a point-in-time
**fork**:

1. Create a branch from the damaged branch with `at` set to a moment *before*
   the incident:

   ```bash
   POST /projects/{prj}/branches
   {"name":"recovery","from_branch":"br_…","at":"2026-07-18T09:14:00Z"}
   ```

2. Connect to the fork and verify the data is what you expected.
3. Copy the affected rows/tables back to the live branch (`pg_dump -t`,
   `COPY`, or application-level repair) — or, for a full rollback, point your
   app at the fork and retire the damaged branch.

This never takes your live database down, and a wrong guess about the
timestamp costs nothing — delete the fork and try another `at`.

## Guarantees & verification

- Recovery works to any point **inside** the retention window; an `at` outside
  it fails the fork (the branch shows `error` — delete and retry within the
  window).
- The platform runs automated restore verification on archives — a backup that
  can't restore pages an operator, rather than being discovered during your
  incident.
- Archives are stored off-cluster in object storage, independent of the
  branch's compute lifecycle (suspended branches stay protected).

## What backups are not

Backups protect against data loss and bad writes. They are not an audit trail
(see *Audit log*) and not a substitute for testing migrations on a **fork**
first — which is one POST away.
