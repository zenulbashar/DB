---
title: API basics — errors, idempotency, pagination
category: Reference
order: 1
summary: The conventions every NimbusDB API call shares.
---

Base URL: `https://api.db.nimbus.app/v1` (or your deployment's URL). All
requests are JSON over TLS with `Authorization: Bearer ndb_…`.

## Errors — RFC 9457 problems

Failures return `application/problem+json`:

```json
{
  "title": "illegal state transition",
  "status": 409,
  "detail": "branch br_… is provisioning; suspend requires ready",
  "request_id": "req_01k…"
}
```

- **Always log `request_id`** — it's the correlation handle your operator can
  trace end-to-end.
- `401` bad/revoked key · `403` key lacks a scope · `404` missing **or not
  yours** (existence isn't leaked across tenants) · `409` conflict/illegal
  state · `422` validation.

## Idempotency

State-creating POSTs accept an `Idempotency-Key` header:

```bash
curl -X POST …/projects -H "Idempotency-Key: $(uuidgen)" …
```

Retrying with the same key replays the stored response instead of creating a
duplicate — make every create in automation retry-safe this way. Lifecycle
actions (`suspend`, `resume`, `resize`) are idempotent by design and don't
need the header.

## Pagination

List endpoints take `limit` (1–100, default 25) and `cursor`, and return
`next_cursor`:

```bash
GET /projects?limit=100
GET /projects?limit=100&cursor=<next_cursor from previous page>
```

`next_cursor: null` means you've reached the end.

## States are the contract

Resources expose a single `state` field (`provisioning`, `ready`,
`suspending`, `suspended`, `resuming`, `resizing`, `error`, `deleting`).
Transitional states mean the platform is converging — poll the resource rather
than retrying the action; actions from a wrong state return `409` and say what
state was required.
