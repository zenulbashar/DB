"use server";

import { revalidatePath } from "next/cache";
import { serverClient, friendlyError } from "@/lib/api";

export type ActionResult = { error?: string; ok?: boolean };

// One helper for the branch lifecycle POSTs (suspend/resume/resize): they all
// return the updated Branch and want the detail page re-fetched on success.
async function branchAction(
  prjId: string,
  path: string,
  body?: unknown,
): Promise<ActionResult> {
  try {
    const client = await serverClient();
    await client.request("POST", path, body);
    revalidatePath(`/projects/${prjId}`);
    return { ok: true };
  } catch (e) {
    return { error: friendlyError(e) };
  }
}

export async function suspendBranch(
  prjId: string,
  brId: string,
): Promise<ActionResult> {
  return branchAction(prjId, `/branches/${brId}/suspend`);
}

export async function resumeBranch(
  prjId: string,
  brId: string,
): Promise<ActionResult> {
  return branchAction(prjId, `/branches/${brId}/resume`);
}

export async function resizeBranch(
  prjId: string,
  brId: string,
  cu: number,
): Promise<ActionResult> {
  if (!Number.isFinite(cu) || cu <= 0) {
    return { error: "Enter a compute size in CU." };
  }
  return branchAction(prjId, `/branches/${brId}/resize`, { cu });
}

export type CreateBranchState = { error?: string };

export async function createBranch(
  prjId: string,
  _prev: CreateBranchState,
  formData: FormData,
): Promise<CreateBranchState> {
  const name = String(formData.get("name") ?? "").trim();
  if (!name) return { error: "Branch name is required." };
  const role = String(formData.get("role") ?? "development");
  const fromBranch = String(formData.get("from_branch") ?? "").trim();
  const body: Record<string, unknown> = { name, role };
  // A branch is a data fork of its parent; empty means the project default.
  if (fromBranch) body.from_branch = fromBranch;
  try {
    const client = await serverClient();
    await client.request("POST", `/projects/${prjId}/branches`, body);
  } catch (e) {
    return { error: friendlyError(e) };
  }
  revalidatePath(`/projects/${prjId}`);
  return {};
}
