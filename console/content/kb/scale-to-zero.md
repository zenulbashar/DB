---
title: Scale to zero — suspend & wake
category: Compute & scaling
order: 1
summary: How branches suspend when idle, wake on the first connection, and how to tune or disable it.
---

Branches you aren't using don't burn compute. When a branch has been idle past
its **suspend timeout**, the platform scales its compute to zero; the first
incoming connection wakes it back up automatically.

## What "suspended" means

- Compute (the Postgres processes) is stopped. **Storage is untouched** — your
  data, roles, and settings are exactly as you left them.
- Endpoints keep their hostnames and show state `suspended`.
- You pay for storage only while suspended.

## Wake on connect

You don't have to resume manually. When a client connects to a suspended
endpoint, the connection is **held open while the branch wakes**, then handed
to Postgres — most drivers just see a slow first connect. Practical notes:

- Set your client's **connect timeout to ≥ 30 s** for branches that may sleep
  (Gen-1 wake target: p95 under 25 s; typically much less).
- A burst of simultaneous connections triggers **one** wake, not many.
- You can also wake explicitly: `POST /branches/{br}/resume` (or the console's
  **Resume** button) — useful as a pre-warm before a traffic spike or right
  after a deploy.

## Idle detection

Activity is measured at the platform's gateway across **all** connections to
the branch. A branch suspends only when the whole platform agrees it has been
idle for longer than `suspend_timeout_s`. Manual suspend is always available:
`POST /branches/{br}/suspend` or the console's **Suspend** button.

## Tuning

`suspend_timeout_s` is per-branch (`PATCH /branches/{br}`):

| Value | Behavior |
|---|---|
| `0` | **Auto-suspend disabled** — the branch never sleeps on its own |
| `300` (default) | Sleep after 5 idle minutes — good for previews/dev |
| up to `86400` | Sleep after up to 24 h |

Recommendation: disable auto-suspend (`0`) on latency-critical production
branches; keep it aggressive on previews.

## State reference

`ready → suspending → suspended → resuming → ready`. The transitions are
idempotent: suspending an already-suspended branch (or resuming a ready one) is
a no-op success. A branch that is `provisioning`, `deleting`, or `error`
refuses suspend/resume with `409`.
