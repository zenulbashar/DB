import Link from "next/link";
import { redirect } from "next/navigation";
import {
  adminClient,
  friendlyError,
  getAdminToken,
  type AdminOverview,
  type AdminBranch,
  type OrgUsage,
} from "@/lib/admin";
import { Badge, Card, ErrorNote, StatusDot, type ResourceState } from "@/components/ui";

export const dynamic = "force-dynamic";

type AuditEntry = {
  id: string;
  org_id: string;
  actor_type: string;
  actor_id: string;
  action: string;
  target_type: string;
  target_id: string;
  at: string;
};

// Stat tile — a headline number is a tile, not a chart (dataviz form rule).
function Stat({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="rounded-card border border-edge bg-surface px-4 py-3">
      <div className="text-xl font-semibold tabular-nums tracking-tight">{value}</div>
      <div className="mt-0.5 text-xs text-fg-muted">{label}</div>
    </div>
  );
}

function StateCounts({ counts }: { counts: Record<string, number> }) {
  const entries = Object.entries(counts).sort((a, b) => b[1] - a[1]);
  if (entries.length === 0) return <p className="text-sm text-fg-muted">None.</p>;
  return (
    <div className="flex flex-wrap gap-x-5 gap-y-2">
      {entries.map(([state, n]) => (
        <span key={state} className="flex items-center gap-2 text-sm text-fg-muted">
          <StatusDot state={state as ResourceState} />
          {state} <span className="font-medium text-fg tabular-nums">{n}</span>
        </span>
      ))}
    </div>
  );
}

export default async function AdminHome() {
  if (!(await getAdminToken())) redirect("/admin/connect");
  const client = await adminClient();

  let overview: AdminOverview;
  let orgs: OrgUsage[] = [];
  let attention: AdminBranch[] = [];
  let audit: AuditEntry[] = [];
  try {
    // The overview gates the page; the rest degrade individually below.
    overview = await client.request<AdminOverview>("GET", "/admin/overview");
    [orgs, attention, audit] = await Promise.all([
      client
        .request<{ data: OrgUsage[] }>("GET", "/admin/orgs")
        .then((r) => r.data ?? []),
      client
        .request<{ data: AdminBranch[] }>("GET", "/admin/branches?state=error")
        .then((r) => r.data ?? []),
      client
        .request<{ data: AuditEntry[] }>("GET", "/admin/audit-log?limit=15")
        .then((r) => r.data ?? []),
    ]);
  } catch (e) {
    return <ErrorNote>{friendlyError(e)}</ErrorNote>;
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Platform overview</h1>
        <p className="mt-1 text-sm text-fg-muted">
          Inventory and activity across every tenant. Usage here is allocated
          compute and resource counts; metered billing usage arrives with the
          metrics pipeline.
        </p>
      </div>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="Organizations" value={overview.orgs} />
        <Stat label="Users" value={overview.users} />
        <Stat label="Projects" value={overview.projects} />
        <Stat label="Branches" value={overview.branches} />
        <Stat label="Allocated CU" value={overview.allocated_cu} />
        <Stat label="Active API keys" value={overview.active_api_keys} />
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="Branches by state">
          <StateCounts counts={overview.branches_by_state} />
        </Card>
        <Card title="Imports by state">
          <StateCounts counts={overview.imports_by_state} />
        </Card>
      </div>

      {overview.restore_verify_failures > 0 && (
        <ErrorNote>
          <strong>{overview.restore_verify_failures}</strong> branch
          {overview.restore_verify_failures === 1 ? " has" : "es have"} a{" "}
          <strong>failed restore verification</strong> — the backup archive may
          be unrestorable. Treat as a data-durability page (R-2), not a
          dashboard row.
        </ErrorNote>
      )}

      {attention.length > 0 && (
        <Card title={`Needs attention — ${attention.length} branch(es) in error`}>
          <ul className="space-y-2">
            {attention.map((b) => (
              <li key={b.id} className="flex items-center gap-2 text-sm">
                <StatusDot state="error" />
                <span className="font-medium">{b.org_name}</span>
                <span className="text-fg-muted">
                  / {b.project_name} / {b.name}
                </span>
                <Link
                  href="/admin/branches?state=error"
                  className="ml-auto text-xs text-accent hover:text-accent-hover"
                >
                  open →
                </Link>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Card title="Tenants">
        {orgs.length === 0 ? (
          <p className="text-sm text-fg-muted">No organizations yet.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-edge text-left text-xs text-fg-muted">
                  <th className="py-2 pr-4 font-medium">Organization</th>
                  <th className="py-2 pr-4 font-medium">Plan</th>
                  <th className="py-2 pr-4 font-medium text-right">Members</th>
                  <th className="py-2 pr-4 font-medium text-right">Projects</th>
                  <th className="py-2 pr-4 font-medium text-right">Branches</th>
                  <th className="py-2 pr-4 font-medium text-right">CU</th>
                  <th className="py-2 pr-4 font-medium text-right">Keys</th>
                  <th className="py-2 pr-4 font-medium text-right">Imports</th>
                  <th className="py-2 font-medium">Last activity</th>
                </tr>
              </thead>
              <tbody>
                {orgs.map((u) => (
                  <tr key={u.org.id} className="border-b border-edge/60">
                    <td className="py-2 pr-4">
                      <span className="font-medium">{u.org.name}</span>{" "}
                      <span className="font-mono text-xs text-fg-faint">{u.org.id}</span>
                    </td>
                    <td className="py-2 pr-4">
                      <Badge>{u.org.plan}</Badge>
                    </td>
                    <td className="py-2 pr-4 text-right tabular-nums">{u.members}</td>
                    <td className="py-2 pr-4 text-right tabular-nums">{u.projects}</td>
                    <td className="py-2 pr-4 text-right tabular-nums">{u.branches}</td>
                    <td className="py-2 pr-4 text-right tabular-nums">{u.allocated_cu}</td>
                    <td className="py-2 pr-4 text-right tabular-nums">{u.active_api_keys}</td>
                    <td className="py-2 pr-4 text-right tabular-nums">{u.imports_running}</td>
                    <td className="py-2 text-xs text-fg-muted">
                      {u.last_active_at
                        ? new Date(u.last_active_at).toUTCString().replace(" GMT", "Z")
                        : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title="Recent activity (all tenants)">
        {audit.length === 0 ? (
          <p className="text-sm text-fg-muted">No audit entries yet.</p>
        ) : (
          <ul className="space-y-1.5">
            {audit.map((e) => (
              <li key={e.id} className="flex flex-wrap items-baseline gap-2 text-sm">
                <code className="text-xs text-accent">{e.action}</code>
                <span className="text-fg-muted">
                  {e.target_type} <span className="font-mono text-xs">{e.target_id}</span>
                </span>
                <span className="text-xs text-fg-faint">
                  by {e.actor_type}:{e.actor_id.slice(0, 20)} · org {e.org_id.slice(0, 14)}… ·{" "}
                  {new Date(e.at).toUTCString().replace(" GMT", "Z")}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
