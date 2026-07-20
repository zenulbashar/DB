import { cookies } from "next/headers";
import { createClient, type components } from "@nimbusdb/api-client";
import { API_BASE, friendlyError } from "./api";

// Operator session + client (ADR-018). The NDB_ADMIN_TOKEN is kept in its own
// httpOnly cookie — same discipline as the tenant key (R-16/R-17), completely
// separate from it. Server components / actions only.

const COOKIE = "ndb_admin";

export type AdminOverview = components["schemas"]["AdminOverview"];
export type OrgUsage = components["schemas"]["OrgUsage"];
export type AdminBranch = components["schemas"]["AdminBranch"];

export { friendlyError };

export async function getAdminToken(): Promise<string | undefined> {
  return (await cookies()).get(COOKIE)?.value;
}

export async function setAdminToken(token: string): Promise<void> {
  (await cookies()).set(COOKIE, token, {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax",
    path: "/",
    maxAge: 60 * 60 * 12, // operator sessions are short: 12 h
  });
}

export async function clearAdminToken(): Promise<void> {
  (await cookies()).delete(COOKIE);
}

// adminClient carries the operator token; the API's /admin routes accept only
// this token, so a tenant key in this cookie simply 401s.
export async function adminClient() {
  const token = await getAdminToken();
  return createClient({ baseUrl: API_BASE, apiKey: token });
}
