---
title: API keys & scopes
category: Access & security
order: 1
summary: Creating, scoping, rotating, and revoking ndb_ API keys.
---

Every API call (and the console session) is authenticated by an **API key**:
`ndb_` followed by 64 hex characters, sent as `Authorization: Bearer ndb_…`.

## Creating a key

Keys belong to an organization and carry **scopes** — grant only what the
consumer needs:

```bash
POST /orgs/{org}/api-keys
{
  "name": "ci-deployer",
  "scopes": ["projects:read", "branches:read", "branches:write"],
  "expires_at": "2027-01-01T00:00:00Z"
}
```

The full token is returned **exactly once**. What's stored (and listed later)
is metadata plus the first 12 characters (`prefix`) so you can tell keys apart.

## Scopes

Scopes are `resource:verb` pairs. The ones you'll use most:

| Scope | Grants |
|---|---|
| `projects:read` / `projects:write` | list/view · create/update/delete projects |
| `branches:read` / `branches:write` | view branches · create/suspend/resume/resize/delete |
| `endpoints:read` | view endpoint hosts/states |
| `roles:read` / `roles:write` | list roles · create/reset passwords **and reveal connection strings** |
| `imports:read` / `imports:write` | watch imports · start/abort/cut over |
| `audit:read` | read the org audit log |
| `keys:manage` | create/revoke API keys |
| `members:manage` | add/remove org members |

Note the deliberate coupling: revealing a connection string requires
`roles:write` because a reveal is equivalent to holding the credential.

## Good hygiene

- **One key per consumer** (app, CI job, teammate's CLI) — revocation stays
  surgical and the audit log stays attributable.
- Set `expires_at` on automation keys; rotate on a schedule.
- The console shows `last_used_at` — keys idle for months are revocation
  candidates.
- Revoke with `DELETE /orgs/{org}/api-keys/{key}`; it takes effect
  immediately.

## If a key leaks

1. Revoke it immediately.
2. Check the **audit log** for actions by that key (entries carry the key as
   the actor).
3. Reset any role passwords it may have revealed (`roles:write` keys).
