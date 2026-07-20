"use server";

import { redirect } from "next/navigation";
import { createClient, ApiError } from "@nimbusdb/api-client";
import { API_BASE } from "@/lib/api";
import { setAdminToken, clearAdminToken } from "@/lib/admin";

export type AdminConnectState = { error?: string };

// Validate the pasted operator token against the admin surface before
// persisting it — a wrong token fails here, not on every page.
export async function adminConnect(
  _prev: AdminConnectState,
  formData: FormData,
): Promise<AdminConnectState> {
  const token = String(formData.get("token") ?? "").trim();
  if (!token) return { error: "Enter the operator token." };
  try {
    await createClient({ baseUrl: API_BASE, apiKey: token }).request(
      "GET",
      "/admin/overview",
    );
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      return {
        error:
          "Token rejected. Check NDB_ADMIN_TOKEN on the control plane (the surface is disabled when it's unset).",
      };
    }
    return { error: "Could not reach the control plane. Is the API running?" };
  }
  await setAdminToken(token);
  redirect("/admin");
}

export async function adminSignOut(): Promise<void> {
  await clearAdminToken();
  redirect("/admin/connect");
}
