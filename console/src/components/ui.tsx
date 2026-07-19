/*
 * Seed primitives for the console design system (DESIGN_SYSTEM_MAPPING.md §3).
 * Variant-map pattern follows the Nimbus ui.tsx idiom; states (loading etc.)
 * follow the design-handoff state catalogue. Grows with Phase 3.
 */
import type { ReactNode } from "react";

const buttonVariants = {
  primary:
    "bg-accent text-white hover:bg-accent-hover disabled:bg-edge disabled:text-fg-faint",
  secondary:
    "border border-edge-strong bg-surface text-fg hover:border-fg-muted disabled:text-fg-faint",
  ghost: "text-fg-muted hover:bg-surface-raised hover:text-fg",
  danger: "bg-danger text-white hover:opacity-90 disabled:opacity-50",
} as const;

const buttonSizes = {
  sm: "h-7 px-2.5 text-xs",
  md: "h-9 px-4 text-sm",
  lg: "h-11 px-5 text-sm",
} as const;

export function Button({
  variant = "primary",
  size = "md",
  loading = false,
  disabled,
  children,
  ...rest
}: {
  variant?: keyof typeof buttonVariants;
  size?: keyof typeof buttonSizes;
  loading?: boolean;
  children: ReactNode;
} & React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      className={`inline-flex items-center justify-center gap-2 rounded-control font-medium transition-colors ${buttonVariants[variant]} ${buttonSizes[size]}`}
      disabled={disabled || loading}
      {...rest}
    >
      {loading && <Spinner />}
      {children}
    </button>
  );
}

export function Card({
  title,
  children,
}: {
  title?: string;
  children: ReactNode;
}) {
  return (
    <section className="rounded-card border border-edge bg-surface p-6">
      {title && <h2 className="mb-3 text-sm font-semibold">{title}</h2>}
      {children}
    </section>
  );
}

export type ResourceState =
  | "provisioning"
  | "ready"
  | "suspending"
  | "suspended"
  | "resuming"
  | "resizing"
  | "error"
  | "deleting";

// Transitional states reuse the settled color they're heading to/from; the ones
// that are actively converging pulse (DESIGN_SYSTEM_MAPPING §2).
const stateColors: Record<ResourceState, string> = {
  provisioning: "bg-state-provisioning",
  ready: "bg-state-ready",
  suspending: "bg-state-suspended",
  suspended: "bg-state-suspended",
  resuming: "bg-state-provisioning",
  resizing: "bg-state-provisioning",
  error: "bg-state-error",
  deleting: "bg-state-deleting",
};

const pulsing = new Set<ResourceState>(["provisioning", "suspending", "resuming", "resizing", "deleting"]);

export function StatusDot({ state }: { state: ResourceState }) {
  return (
    <span
      aria-label={state}
      className={`inline-block size-2 rounded-pill ${stateColors[state] ?? "bg-state-error"} ${
        pulsing.has(state) ? "animate-pulse" : ""
      }`}
    />
  );
}

export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="rounded-card border border-dashed border-edge px-6 py-10 text-center">
      <p className="text-sm font-medium">{title}</p>
      {hint && <p className="mt-1 text-sm text-fg-muted">{hint}</p>}
    </div>
  );
}

export function ErrorNote({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger" role="alert">
      {children}
    </div>
  );
}

export function Badge({ children }: { children: ReactNode }) {
  return (
    <span className="rounded-pill border border-edge px-2 py-0.5 text-xs text-fg-muted">
      {children}
    </span>
  );
}

export function Spinner() {
  return (
    <span
      aria-hidden
      className="size-3.5 animate-spin rounded-pill border-2 border-white/30 border-t-white"
    />
  );
}

export function ConnectionString({ value }: { value: string }) {
  // Secret-bearing display: masked by default; the audited reveal flow
  // arrives with the API wiring in Phase 3.
  const masked = value.replace(/:\/\/([^:]+):[^@]+@/, "://$1:••••••••@");
  return (
    <code className="block overflow-x-auto rounded-control border border-edge bg-background px-3 py-2 font-mono text-xs text-fg-muted">
      {masked}
    </code>
  );
}
