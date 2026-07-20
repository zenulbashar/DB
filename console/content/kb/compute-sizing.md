---
title: Compute sizing & resize
category: Compute & scaling
order: 2
summary: CU bounds, live vertical resize, and how autoscaling uses them.
---

Branch compute is measured in **CU** (compute units — a bundle of CPU and
memory). Each branch declares a range, and runs at a size inside it:

| Field | Meaning |
|---|---|
| `min_cu` | Floor — the size a branch wakes at (0.25–8) |
| `max_cu` | Ceiling — the largest it may be resized to |
| `current_cu` | The actual running size right now |

New branches default to **0.25–2 CU**. Change bounds any time with
`PATCH /branches/{br}` (`compute_min_cu`, `compute_max_cu`).

## Resizing

Resize a running branch without downtime:

```bash
POST /branches/{br}/resize   {"cu": 2}
```

or the **Resize** control on the console's branch card. What happens:

1. The requested size is **clamped** to `[min_cu, max_cu]`.
2. The branch goes `ready → resizing`; connections keep flowing (on HA
   branches the replica is resized first, then a switchover).
3. The branch returns to `ready` at the new `current_cu`.

Resizing to the size you're already at is a no-op success. A branch that isn't
`ready` (e.g. suspended) returns `409` — resume it first.

## Autoscaling

The resize action is the same lever the platform's autoscaler will drive from
metrics (CPU/memory pressure) — set honest `min_cu`/`max_cu` bounds and the
autoscaler works inside them. Until metrics-driven autoscaling is enabled on
your deployment, resize manually or from your own automation (it's one POST).

## Choosing sizes

- **Previews/dev:** leave the defaults; scale-to-zero matters more than size.
- **Production:** set `min_cu` to what your steady-state load needs (watch
  CPU at your current size), and `max_cu` to your spike budget.
- Memory-bound workloads (large sorts, many connections on the direct
  endpoint) benefit more from a bigger CU than CPU-bound ones — pooled
  connections keep backend counts low.
