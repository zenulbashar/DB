---
title: Audit log
category: Access & security
order: 3
summary: Every state-changing action is recorded — what's in an entry and how to use it.
---

Every state-changing action in your organization is written to an append-only
**audit log**: project and branch lifecycle, key issuance and revocation,
member changes, imports, secret reveals, and operator interventions.

## Reading it

```bash
GET /orgs/{org}/audit-log?limit=50
```

(requires the `audit:read` scope). Entries are newest-first and paginated with
`next_cursor`.

## What an entry contains

| Field | Meaning |
|---|---|
| `actor_type` / `actor_id` | who: an `api_key` (yours), a `user`, or `system` (the platform/operator) |
| `action` | what: dotted verbs like `project.create`, `branch.suspend`, `api_key.revoke`, `connection_uri.reveal` |
| `target_type` / `target_id` | the resource acted on |
| `ip` | caller IP where applicable |
| `at` | timestamp |
| `details` | action-specific context |

## Things the audit log answers

- *"Who revealed the production connection string?"* — look for
  `connection_uri.reveal`; the actor is the API key that did it.
- *"Why did the branch suspend at 3am?"* — a `branch.suspend` by `system` is
  the idle detector; by an `api_key` it was manual.
- *"Did the operator touch our project?"* — platform-operator actions are
  logged into **your** org's audit log with `actor_type: system`, so tenant
  visibility of interventions is built in.
- *"What did the leaked key do?"* — filter by the key's actor id after a
  revocation.

## Properties

- Entries are written by the platform in the same transaction as the action —
  an action you can see happened has its entry.
- The log is append-only; nobody (including operators) can edit or delete
  entries through any API.
- Retention follows your plan's compliance window.
