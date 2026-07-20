"use server";

import { revalidatePath } from "next/cache";
import { adminClient, friendlyError } from "@/lib/admin";

export type AdminActionResult = { error?: string; ok?: boolean };

// Operator fix actions (ADR-018): same tenant state machine underneath; the
// control plane writes each one to the tenant's audit log.
async function fixAction(path: string, body?: unknown): Promise<AdminActionResult> {
  try {
    const client = await adminClient();
    await client.request("POST", path, body);
    revalidatePath("/admin");
    revalidatePath("/admin/branches");
    return { ok: true };
  } catch (e) {
    return { error: friendlyError(e) };
  }
}

export async function adminSuspendBranch(brID: string): Promise<AdminActionResult> {
  return fixAction(`/admin/branches/${brID}/suspend`);
}

export async function adminResumeBranch(brID: string): Promise<AdminActionResult> {
  return fixAction(`/admin/branches/${brID}/resume`);
}

export async function adminResizeBranch(
  brID: string,
  cu: number,
): Promise<AdminActionResult> {
  if (!Number.isFinite(cu) || cu <= 0) {
    return { error: "Enter a compute size in CU." };
  }
  return fixAction(`/admin/branches/${brID}/resize`, { cu });
}
