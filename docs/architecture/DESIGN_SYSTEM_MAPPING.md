# Design System Mapping — NimbusDB Console

**Status:** Draft v0.1 · **Open input:** no dedicated design export for the NimbusDB console has been provided yet (see §5). This document fixes the interim design direction and the mapping method so a future export drops in without rework.

---

## 1. Design inputs discovered

| Source | What it is | Relevance |
|---|---|---|
| **Nimbus design language** (`hosting/src/app/globals.css`, `hosting/src/components/ui.tsx`) | Tailwind v4 `@theme` tokens: dark UI (`--color-background #0a0a0a`, surface/edge scales), accent blue `#0070f3`, signature forest-green header (`--color-forest #13301f`), success/warning/danger tokens, `--font-sans/--font-mono`; bespoke primitives: `Button`, `ButtonLink`, `Input`, `Label`, `Select`, `Card`, `Badge`, `StatusDot`, `Spinner`, `EmptyState`, `PageHeader`. | **Adopted as the console's base design language** (ADR-009): NimbusDB is part of the Nimbus platform family; a database console reached from Nimbus must feel native to it. |
| **Prompt2Eat design handoff** (`order-tool/design/design_handoff_prompt2eat/`) | High-fidelity export: `tokens.css` (CSS custom properties + keyframes), `tailwind.theme.js`, component state catalogue (buttons/inputs × default/hover/focus/loading/disabled), screens, per-block PNGs. | **Not** the console's visual identity (it is Prompt2Eat's product brand — cream/amber/forest ink). Adopted as the **handoff format and token-layer method**: its `tokens.css` structure (framework-agnostic custom properties → Tailwind `@theme`) is exactly how the console's token layer is built, and any future NimbusDB design export is expected in this bundle shape. |

## 2. Token layer (`/console/src/app/globals.css`, Tailwind v4 `@theme`)

Interim palette = Nimbus tokens, extended with what a data-dense console needs. Prefix-free
Tailwind theme variables (Nimbus convention), documented here as the source of truth:

```
--color-background #0a0a0a      --color-surface (raised)        --color-edge / edge-strong
--color-fg / fg-muted / fg-faint
--color-accent #0070f3          (primary actions, links, active nav)
--color-forest #13301f          (brand chrome: top bar, marketing accents)
--color-success / warning / danger  (status system, reused for branch/endpoint states)
--font-sans / --font-mono       (mono is load-bearing: connection strings, SQL, IDs)
radii: buttons 6px · inputs 6px · cards 10px · pills 999px   (Nimbus's existing feel)
```

Console-specific token additions (new, versioned here first):
- **State colors** for resource lifecycle: `provisioning` (accent, pulsing), `ready` (success),
  `suspended` (fg-muted), `error` (danger), `deleting` (warning) — used by `StatusDot`, badges,
  and charts identically.
- **Chart palette**: 6-step categorical ramp derived from accent/success/warning + neutrals,
  AA-contrast on dark surfaces (final values set with the dataviz pass in Phase 3).
- **Density scale**: `--space-row-compact` etc. for data tables (consoles are denser than
  marketing UIs; Nimbus's spacing is kept for page chrome, tables get a compact scale).

Light mode: not in Nimbus today; console ships dark-first (parity), with tokens structured so a
`prefers-color-scheme` light theme is a token-swap, not a refactor (the P2E handoff demonstrates
the dual-theme pattern we follow).

## 3. Primitive mapping (console component → base)

| Console need | Base | Gap to build |
|---|---|---|
| Buttons/links, variants+sizes | Nimbus `Button`/`ButtonLink` pattern (variant map) | add `loading` state (spinner-in-button, P2E handoff has the reference states) |
| Form controls | Nimbus `Input`, `Label`, `Select` | add `Textarea`, `Combobox` (region/PG-version pickers), inline validation states |
| Cards/panels | Nimbus `Card` | add header/footer slots, stat-tile variant (usage dashboards) |
| Status | Nimbus `Badge`, `StatusDot` | extend with lifecycle state set (§2) |
| Empty/loading | Nimbus `EmptyState`, `Spinner` | add skeleton rows (shimmer keyframe per handoff method) |
| Page scaffold | Nimbus `PageHeader` + nav shell | project/branch switcher (org → project → branch breadcrumb) |
| **DataTable** | — (new) | sortable/paginated table for branches, backups, roles, audit log; compact density; column presets |
| **CodeBlock / ConnectionString** | — (new) | mono, one-click copy, secret masking with audited reveal |
| **SQL editor** | — (new) | CodeMirror 6, SQL dialect, schema-aware autocomplete (from information_schema), result grid = DataTable |
| **Charts** | — (new) | time-series (uPlot or Recharts; pick in Phase 3 with the dataviz standards) for metrics dashboards |
| **Modal/Drawer, Toast, Tabs, Tooltip** | — (new) | headless (Radix or hand-rolled per Nimbus's bespoke preference — decided Phase 3); toast slide-in per handoff motion spec |
| **Diff/plan view** | — (new, Phase 5) | migration cutover checklists, import progress |

Motion: follow the handoff's motion discipline (150–250 ms ease-out; spinner .7 s linear;
focus rings on every interactive element — console uses accent-blue ring on dark).

## 4. Surface inventory (what the console must render, by phase)

- **P3:** auth screens, org/project dashboard, project overview (branch list + state), connection
  panel, roles & databases, SQL editor, metrics dashboards, API keys, audit log, settings.
- **P4:** branch graph view (lineage), restore/branch dialogs with PITR time picker,
  compute/autoscale settings, replica management.
- **P5:** import wizard (source → preflight report → sync progress → cutover checklist).
- **P6:** Nimbus integration panel (link org, deploy actions, attach/detach), env-injection status.
- **P7:** usage & billing, plan management, query insights, admin portal (separate app shell,
  same primitives, visually distinct chrome — forest header swapped for a neutral "operator" chrome).

Accessibility bar (phase-gate item from P3): keyboard-complete flows, visible focus, AA contrast
on all text/status colors, `prefers-reduced-motion` respected.

## 5. Pending design export (explicit open item)

The mission statement references a design export to be provided for this platform. It has not
yet appeared in this repository or the analysed repos (`order-tool` contains only Prompt2Eat's
product design bundle). **Process when it arrives (docs-first rule):**

1. Place bundle under `/docs/design/<export-name>/` (expected shape: the P2E handoff format —
   `README.md` token tables, `tokens.css`, `tailwind.theme.js`, screens, block PNGs).
2. Update §2 token values + §3 mapping in this document in the same PR.
3. Only then adjust `/console` theme + primitives.

Until then, Phase 3 proceeds on the Nimbus-derived interim system above — deliberately
token-isolated so a re-skin is contained in the token layer and primitive styles.

## 6. Implementation status (updated per increment)

Built and live-wired (console read surface):
- **Primitives:** `Button` (with `loading`), `Card`, `Badge`, `StatusDot` (all 8 lifecycle states;
  transitional states pulse), `Spinner`, `EmptyState`, `ErrorNote`, `ConnectionString` (masking),
  `CopyField` (mono value + one-click copy, the CodeBlock/ConnectionString §3 gap's first slice).
- **Screens:** connect (§4 auth), projects dashboard, project overview (branch list + endpoints +
  connection panel).

Not yet built (later Phase 3 slices): `DataTable`, `Modal/Drawer`, `Toast`, `Tabs`, `Tooltip`,
`Textarea`/`Combobox`, skeleton loaders, charts, the SQL editor, and the metrics/audit/role/API-key
management screens. The token layer and re-skin isolation (§5) are unchanged by this slice.
