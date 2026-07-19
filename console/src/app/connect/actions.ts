"use server";

import { redirect } from "next/navigation";
import { createClient, ApiError } from "@nimbusdb/api-client";
import { API_BASE } from "@/lib/api";
import { setToken, clearToken } from "@/lib/session";

export type ConnectState = { error?: string };

// connect validates the pasted API key against the control plane (any
// authenticated call) before persisting it, so a bad key fails here rather than
// on every page.
export async function connect(
  _prev: ConnectState,
  formData: FormData,
): Promise<ConnectState> {
  const token = String(formData.get("token") ?? "").trim();
  if (!token.startsWith("ndb_")) {
    return { error: "Enter a valid API key (starts with ndb_)." };
  }
  try {
    await createClient({ baseUrl: API_BASE, apiKey: token }).listProjects({ limit: 1 });
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      return { error: "That API key was rejected by the control plane." };
    }
    if (e instanceof ApiError && e.status === 403) {
      return { error: "That key lacks the projects:read scope needed to sign in." };
    }
    return { error: "Could not reach the control plane. Check that the API is running." };
  }
  await setToken(token);
  redirect("/");
}

export async function signOut(): Promise<void> {
  await clearToken();
  redirect("/connect");
}
