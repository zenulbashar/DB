"use server";

import { serverClient, friendlyError } from "@/lib/api";

// The owner-role password is returned by POST /projects exactly once. We hand it
// straight back to the client component's action state (in memory, never
// persisted) so the one-time reveal panel can show it, then it's gone.
export type CreatedProject = {
  projectId: string;
  projectName: string;
  roleName: string;
  password: string;
  database: string;
};

export type CreateProjectState = { error?: string; created?: CreatedProject };

type ProjectCreated = {
  project: { id: string; name: string };
  owner_role: { name: string; password: string };
  database: { name: string };
};

export async function createProject(
  _prev: CreateProjectState,
  formData: FormData,
): Promise<CreateProjectState> {
  const org_id = String(formData.get("org_id") ?? "").trim();
  const name = String(formData.get("name") ?? "").trim();
  const region = String(formData.get("region") ?? "syd1");
  const pg_version = Number(formData.get("pg_version") ?? 17);
  if (!org_id) return { error: "Select an organization." };
  if (!name) return { error: "Project name is required." };

  try {
    const client = await serverClient();
    const res = await client.request<ProjectCreated>("POST", "/projects", {
      org_id,
      name,
      region,
      pg_version,
    });
    return {
      created: {
        projectId: res.project.id,
        projectName: res.project.name,
        roleName: res.owner_role.name,
        password: res.owner_role.password,
        database: res.database.name,
      },
    };
  } catch (e) {
    return { error: friendlyError(e) };
  }
}
