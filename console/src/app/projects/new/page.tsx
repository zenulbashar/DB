import Link from "next/link";
import { redirect } from "next/navigation";
import { getToken } from "@/lib/session";
import { serverClient, friendlyError } from "@/lib/api";
import { ErrorNote } from "@/components/ui";
import { NewProjectForm } from "./new-project-form";

export const dynamic = "force-dynamic";

type Org = { id: string; name: string };

export default async function NewProjectPage() {
  if (!(await getToken())) redirect("/connect");
  const client = await serverClient();

  let orgs: Org[] = [];
  let error: string | undefined;
  try {
    const res = await client.request<{ data: Org[] }>("GET", "/orgs");
    orgs = res.data ?? [];
  } catch (e) {
    error = friendlyError(e);
  }

  return (
    <div className="mx-auto max-w-md space-y-4">
      <Link
        href="/"
        className="text-xs text-fg-muted transition-colors hover:text-fg"
      >
        ← Projects
      </Link>
      {error ? (
        <ErrorNote>{error}</ErrorNote>
      ) : orgs.length === 0 ? (
        <ErrorNote>
          No organization is available for this API key (needs the{" "}
          <code>orgs:read</code> scope).
        </ErrorNote>
      ) : (
        <NewProjectForm orgs={orgs} />
      )}
    </div>
  );
}
