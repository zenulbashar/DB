"use client";

import { useActionState } from "react";
import {
  Button,
  ButtonLink,
  Card,
  ErrorNote,
  Field,
  Input,
  Select,
} from "@/components/ui";
import { CopyField } from "@/components/copy";
import { createProject, type CreateProjectState } from "./actions";

export function NewProjectForm({
  orgs,
}: {
  orgs: Array<{ id: string; name: string }>;
}) {
  const [state, action, pending] = useActionState<CreateProjectState, FormData>(
    createProject,
    {},
  );

  // One-time reveal: after creation, swap the form for the credentials panel.
  // The password lives only in this component's state for this page view.
  if (state.created) {
    const c = state.created;
    return (
      <Card title={`Created ${c.projectName}`}>
        <div className="mb-4 rounded-control border border-warning/40 bg-warning/10 px-3 py-2 text-sm text-warning">
          Save the password now — it is shown <strong>exactly once</strong> and
          cannot be retrieved later (reset it if you lose it).
        </div>
        <div className="space-y-3">
          <CopyField label="Database" value={c.database} />
          <CopyField label="Owner role" value={c.roleName} />
          <CopyField label="Password" value={c.password} />
        </div>
        <div className="mt-5 flex gap-3">
          <ButtonLink href={`/projects/${c.projectId}`}>Open project</ButtonLink>
          <ButtonLink href="/" variant="ghost">
            Back to projects
          </ButtonLink>
        </div>
      </Card>
    );
  }

  return (
    <Card title="New project">
      <form action={action} className="space-y-3">
        <Field label="Organization">
          <Select name="org_id" defaultValue={orgs[0]?.id ?? ""} required>
            {orgs.map((o) => (
              <option key={o.id} value={o.id}>
                {o.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Name">
          <Input name="name" placeholder="my-app" required autoComplete="off" />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Region">
            <Select name="region" defaultValue="syd1">
              <option value="syd1">syd1</option>
            </Select>
          </Field>
          <Field label="Postgres">
            <Select name="pg_version" defaultValue="17">
              <option value="17">17</option>
              <option value="16">16</option>
            </Select>
          </Field>
        </div>
        {state.error && <ErrorNote>{state.error}</ErrorNote>}
        <Button type="submit" loading={pending}>
          {pending ? "Creating…" : "Create project"}
        </Button>
      </form>
    </Card>
  );
}
