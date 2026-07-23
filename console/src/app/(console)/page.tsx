import Link from "next/link";
import { redirect } from "next/navigation";
import { getToken } from "@/lib/session";
import { serverClient, friendlyError, type Project } from "@/lib/api";
import {
  Badge,
  ButtonLink,
  EmptyState,
  ErrorNote,
  StatusDot,
  type ResourceState,
} from "@/components/ui";

// Live data; never statically cached (the API key lives in the request cookie).
export const dynamic = "force-dynamic";

// Projects carry their own lifecycle enum (`pending` before the reconciler
// picks them up); map it onto the shared status palette.
function dotState(state: string): ResourceState {
  return state === "pending" ? "provisioning" : (state as ResourceState);
}

export default async function Home() {
  if (!(await getToken())) redirect("/connect");

  const client = await serverClient();
  let projects: Project[] = [];
  let error: string | undefined;
  try {
    const res = await client.listProjects({ limit: 100 });
    projects = res.data ?? [];
  } catch (e) {
    error = friendlyError(e);
  }

  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Projects</h1>
          <p className="mt-1 text-sm text-fg-muted">
            Serverless PostgreSQL by Zale IT.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <Badge>{projects.length}</Badge>
          <ButtonLink href="/projects/new" size="sm">
            New project
          </ButtonLink>
        </div>
      </div>

      {error && <ErrorNote>{error}</ErrorNote>}

      {!error && projects.length === 0 && (
        <EmptyState
          title="No projects yet"
          hint="Create one with the control-plane API or CLI; provisioning UI lands next."
        />
      )}

      {projects.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2">
          {projects.map((p) => (
            <Link
              key={p.id}
              href={`/projects/${p.id}`}
              className="block rounded-card border border-edge bg-surface p-6 transition-colors hover:border-edge-strong"
            >
              <div className="flex items-center justify-between gap-3">
                <h2 className="truncate text-sm font-semibold">{p.name}</h2>
                <Badge>PG {p.pg_version}</Badge>
              </div>
              <div className="mt-3 flex items-center gap-2 text-sm text-fg-muted">
                <StatusDot state={dotState(p.state)} />
                {p.state}
                <span className="text-fg-faint">·</span>
                {p.region}
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
