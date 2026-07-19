import { createClient, ApiError, type components } from "@nimbusdb/api-client";
import { getToken } from "./session";

// Single API base for the whole console (ADR-012 — one client, no ad-hoc fetches).
export const API_BASE = process.env.NDB_API_URL ?? "http://localhost:8080/v1";

export type Project = components["schemas"]["Project"];
export type Branch = components["schemas"]["Branch"];
export type Endpoint = components["schemas"]["Endpoint"];

export { ApiError };

// serverClient builds a request-scoped client carrying the caller's API key
// from their cookie. Server components / actions only.
export async function serverClient() {
  const apiKey = await getToken();
  return createClient({ baseUrl: API_BASE, apiKey });
}

// friendlyError turns any thrown value into a message safe to render.
export function friendlyError(e: unknown): string {
  if (e instanceof ApiError) {
    return e.problem.detail || e.problem.title || `Request failed (${e.status})`;
  }
  if (e instanceof Error && e.message.includes("fetch failed")) {
    return "Could not reach the control plane. Is the API running at NDB_API_URL?";
  }
  return "Something went wrong talking to the control plane.";
}
