import Link from "next/link";
import { redirect } from "next/navigation";
import {
  adminClient,
  friendlyError,
  getAdminToken,
  type AdminBranch,
} from "@/lib/admin";
import { Badge, Card, ErrorNote, StatusDot, type ResourceState } from "@/components/ui";
import { FixActions } from "./fix-actions";

export const dynamic = "force-dynamic";

const STATES = [
  "",
  "provisioning",
  "ready",
  "suspending",
  "suspended",
  "resuming",
  "resizing",
  "error",
] as const;

export default async function AdminBranches({
  searchParams,
}: {
  searchParams: Promise<{ state?: string }>;
}) {
  if (!(await getAdminToken())) redirect("/admin/connect");
  const { state = "" } = await searchParams;
  const client = await adminClient();

  let branches: AdminBranch[] = [];
  let error: string | undefined;
  try {
    const qs = state ? `?state=${encodeURIComponent(state)}` : "";
    const res = await client.request<{ data: AdminBranch[] }>(
      "GET",
      `/admin/branches${qs}`,
    );
    branches = res.data ?? [];
  } catch (e) {
    error = friendlyError(e);
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Branches</h1>
          <p className="mt-1 text-sm text-fg-muted">
            Every tenant branch. Fix actions drive the tenant state machine and
            are written to the tenant&apos;s audit log.
          </p>
        </div>
        <nav className="flex flex-wrap gap-1.5" aria-label="Filter by state">
          {STATES.map((s) => (
            <Link
              key={s || "all"}
              href={s ? `/admin/branches?state=${s}` : "/admin/branches"}
              className={`rounded-pill border px-2.5 py-0.5 text-xs transition-colors ${
                s === state
                  ? "border-accent text-fg"
                  : "border-edge text-fg-muted hover:border-edge-strong hover:text-fg"
              }`}
            >
              {s || "all"}
            </Link>
          ))}
        </nav>
      </div>

      {error && <ErrorNote>{error}</ErrorNote>}
      {!error && branches.length === 0 && (
        <p className="text-sm text-fg-muted">
          No branches{state ? ` in state “${state}”` : ""}.
        </p>
      )}

      <div className="space-y-3">
        {branches.map((b) => (
          <Card key={b.id}>
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <div className="flex flex-wrap items-center gap-2">
                  <StatusDot state={b.state as ResourceState} />
                  <span className="text-sm font-medium">{b.name}</span>
                  <Badge>{b.role}</Badge>
                  <span className="text-xs text-fg-muted">{b.state}</span>
                </div>
                <div className="mt-1.5 text-xs text-fg-muted">
                  <span className="font-medium text-fg">{b.org_name}</span> /{" "}
                  {b.project_name} ·{" "}
                  <span className="font-mono text-fg-faint">{b.id}</span> ·{" "}
                  {b.compute.min_cu}–{b.compute.max_cu} CU
                  {b.compute.current_cu ? ` · at ${b.compute.current_cu}` : ""}
                </div>
              </div>
              <FixActions
                brID={b.id}
                state={b.state}
                minCu={b.compute.min_cu}
                maxCu={b.compute.max_cu}
                currentCu={b.compute.current_cu ?? 0}
              />
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}
