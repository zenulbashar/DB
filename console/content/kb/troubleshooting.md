---
title: Troubleshooting
category: Reference
order: 2
summary: The most common errors and what actually fixes them.
---

## Connection problems

**First connection is slow / times out after a quiet period.**
The branch was suspended and is waking. Raise your client's connect timeout to
≥ 30 s, or disable auto-suspend on that branch (`suspend_timeout_s: 0`). See
*Scale to zero*.

**`password authentication failed`.**
Passwords are reveal-once; a guessed/stale one won't work. Reset it
(`POST /branches/{br}/roles/{role}/reset-password`), update your secret store,
redeploy.

**`prepared statement "…" does not exist` / `LISTEN` never fires / advisory
lock weirdness.**
You're on the **pooled** endpoint with a session-state feature. Move those
connections to the **direct** endpoint. See *Endpoints — which one do I use?*.

**TLS errors.**
Keep `sslmode=require`; endpoints do not accept plaintext.

## API errors

**`401 unauthorized`** — key missing, malformed (must start with `ndb_`),
revoked, or expired. Check `GET /orgs/{org}/api-keys` for `revoked_at` /
`expires_at`.

**`403 forbidden`** — the key lacks a scope. The error's `detail` names it;
mint a key with the right scopes rather than widening a shared one.

**`404 not found` on something that exists** — you're using a key from a
different organization. Cross-tenant requests 404 by design.

**`409 illegal state transition`** — the resource isn't in the state the
action needs (e.g. resize while suspended). Read the resource, wait out the
transitional state, act again. Poll — don't blind-retry.

## Branch states

**Stuck in `provisioning`.**
Forks restore real data — minutes is normal for large parents. If it exceeds
~30 min, contact your operator with the branch ID (they can see the underlying
cluster).

**`error` after creating a branch with `at`.**
The timestamp is outside the parent's retention window. Delete the branch and
fork again inside the window (see *Backups & PITR*).

## Imports

**Stuck in `live_sync` with lag not dropping** — write rate on the source may
exceed apply rate; try a quieter window or a bigger target CU.

**`cutover` returns 409** — cutover is only legal from `cutover_ready`. Check
`GET /imports/{imp}` for state and error.

## When you contact support

Include: the **resource ID** (`prj_…`, `br_…`, `imp_…`), the **`request_id`**
from the error body, and the timestamp. Operators can trace a `request_id`
through the whole platform — it turns "it's broken" into a five-minute fix.
