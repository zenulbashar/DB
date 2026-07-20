"use client";

import { useActionState } from "react";
import { Button, ErrorNote, Field, Input, Select } from "@/components/ui";
import { createBranch, type CreateBranchState } from "./actions";

// Create-branch form. `from_branch` is the fork parent (a branch is a data
// fork of its parent, ADR-016); it defaults to the project's default branch.
export function NewBranchForm({
  prjId,
  branches,
}: {
  prjId: string;
  branches: Array<{ id: string; name: string }>;
}) {
  const action = createBranch.bind(null, prjId);
  const [state, formAction, pending] = useActionState<CreateBranchState, FormData>(
    action,
    {},
  );

  return (
    <form action={formAction} className="grid gap-3 sm:grid-cols-2">
      <Field label="Name">
        <Input name="name" placeholder="feature-x" required autoComplete="off" />
      </Field>
      <Field label="Role">
        <Select name="role" defaultValue="development">
          <option value="development">development</option>
          <option value="preview">preview</option>
          <option value="production">production</option>
        </Select>
      </Field>
      <Field label="Fork from" hint="Copies the parent's data at branch time.">
        <Select name="from_branch" defaultValue={branches[0]?.id ?? ""}>
          {branches.map((b) => (
            <option key={b.id} value={b.id}>
              {b.name}
            </option>
          ))}
        </Select>
      </Field>
      <div className="flex items-end">
        <Button type="submit" loading={pending}>
          {pending ? "Creating…" : "Create branch"}
        </Button>
      </div>
      {state.error && (
        <div className="sm:col-span-2">
          <ErrorNote>{state.error}</ErrorNote>
        </div>
      )}
    </form>
  );
}
