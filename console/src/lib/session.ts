import { cookies } from "next/headers";

// Phase 3 console auth is API-key based for now: the user pastes an `ndb_` key
// (the complete programmatic path, ADR-013) which we keep in an httpOnly cookie.
// Email magic-link sessions (SECURITY_MODEL §3) layer on later — this is the
// same credential the CLI/Nimbus integration use, so no throwaway auth.
const COOKIE = "ndb_token";

export async function getToken(): Promise<string | undefined> {
  return (await cookies()).get(COOKIE)?.value;
}

export async function setToken(token: string): Promise<void> {
  (await cookies()).set(COOKIE, token, {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax",
    path: "/",
    maxAge: 60 * 60 * 24 * 30,
  });
}

export async function clearToken(): Promise<void> {
  (await cookies()).delete(COOKIE);
}
