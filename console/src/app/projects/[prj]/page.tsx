import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import { ApiError } from "@nimbusdb/api-client";
import { getToken } from "@/lib/session";
import {
  serverClient,
  friendlyError,
  type Branch,
  type Endpoint,
  type Project,
} from "@/lib/api";
import {
  Badge,
  Card,
  EmptyState,
  ErrorNote,
  StatusDot,
  type ResourceState,
} from "@/components/ui";
import { CopyField } from "@/components/copy";

export const dynamic = "force-dynamic";

// The connection-uri endpoint assembles a (masked) URI server-side so the
// console never touches a password unless the user runs the audited reveal.
type ConnUri = {
  uri: string;
  endpoint: string;
  branch_id: string;
  role: string;
  database: string;
  revealed: boolean;
};

function dotState(state: string): ResourceState {
  return state === "pending" ? "provisioning" : (state as ResourceState);
}

const kindLabel: Record<string, string> = {
  rw_direct: "Direct (read/write)",
  rw_pooled: "Pooled (read/write)",
  ro_pooled: "Pooled (read-only)",
};

export default async function ProjectPage({
  params,
}: {
  params: Promise<{ prj: string }>;
}) {
  if (!(await getToken())) redirect("/connect");
  const { prj } = await params;
  const client = await serverClient();

  let project: Project;
  try {
    project = await client.request<Project>("GET", `/projects/${prj}`);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) notFound();
    return <ErrorNote>{friendlyError(e)}</ErrorNote>;
  }

  // Branch list omits endpoints; fetch each branch for its endpoints in parallel.
  let branchErr: string | undefined;
  let branches: Array<Branch & { endpoints: Endpoint[] }> = [];
  try {
    const list = await client.request<{ data: Branch[] }>(
      "GET",
      `/projects/${prj}/branches`,
    );
    branches = await Promise.all(
      (list.data ?? []).map(async (b) => {
        try {
          const full = await client.request<Branch>("GET", `/branches/${b.id}`);
          return { ...b, endpoints: full.endpoints ?? [] };
        } catch {
          return { ...b, endpoints: [] as Endpoint[] };
        }
      }),
    );
  } catch (e) {
    branchErr = friendlyError(e);
  }

  // Masked connection string for the default (pooled read/write) endpoint.
  let conn: ConnUri | undefined;
  let connNote: string | undefined;
  try {
    conn = await client.request<ConnUri>(
      "GET",
      `/projects/${prj}/connection-uri`,
    );
  } catch (e) {
    connNote =
      e instanceof ApiError && (e.status === 409 || e.status === 404)
        ? "A connection string appears here once an endpoint is ready."
        : friendlyError(e);
  }

  return (
    <div className="space-y-6">
      <div>
        <Link
          href="/"
          className="text-xs text-fg-muted transition-colors hover:text-fg"
        >
          ← Projects
        </Link>
        <div className="mt-2 flex items-center gap-3">
          <h1 className="text-xl font-semibold tracking-tight">
            {project.name}
          </h1>
          <StatusDot state={dotState(project.state)} />
          <span className="text-sm text-fg-muted">{project.state}</span>
        </div>
        <div className="mt-2 flex gap-2">
          <Badge>PG {project.pg_version}</Badge>
          <Badge>{project.region}</Badge>
        </div>
      </div>

      <Card title="Connect">
        {conn ? (
          <div className="space-y-3">
            <CopyField label="Connection string" value={conn.uri} />
            <p className="text-xs text-fg-muted">
              Password masked. Reveal via the API with{" "}
              <code className="text-fg">roles:write</code> — every reveal is
              audit-logged.
            </p>
          </div>
        ) : (
          <p className="text-sm text-fg-muted">{connNote}</p>
        )}
      </Card>

      <div>
        <h2 className="mb-3 text-sm font-semibold">Branches</h2>
        {branchErr && <ErrorNote>{branchErr}</ErrorNote>}
        {!branchErr && branches.length === 0 && (
          <EmptyState title="No branches" />
        )}
        <div className="space-y-4">
          {branches.map((b) => (
            <Card key={b.id}>
              <div className="flex flex-wrap items-center gap-2">
                <StatusDot state={dotState(b.state)} />
                <span className="text-sm font-medium">{b.name}</span>
                <Badge>{b.role}</Badge>
                {b.parent === null && <Badge>default</Badge>}
                <span className="ml-auto text-xs text-fg-muted">
                  {b.compute.min_cu}–{b.compute.max_cu} CU
                  {b.compute.current_cu ? ` · at ${b.compute.current_cu}` : ""}
                </span>
              </div>
              <div className="mt-4 space-y-3">
                {b.endpoints.length === 0 ? (
                  <p className="text-xs text-fg-muted">No endpoints yet.</p>
                ) : (
                  b.endpoints.map((ep) => (
                    <div key={ep.id} className="space-y-1">
                      <div className="flex items-center gap-2 text-xs text-fg-muted">
                        <StatusDot state={dotState(ep.state)} />
                        {kindLabel[ep.kind] ?? ep.kind}
                      </div>
                      <CopyField value={ep.host} />
                    </div>
                  ))
                )}
              </div>
            </Card>
          ))}
        </div>
      </div>
    </div>
  );
}
