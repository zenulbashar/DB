/*
 * Thin typed client over the generated OpenAPI schema. The console and the
 * Nimbus integration consume this; no other HTTP client for the Zale DB API
 * should exist in the monorepo (ADR-012).
 */
import type { paths } from "./schema";

export type { paths, components } from "./schema";

export interface ClientOptions {
  baseUrl: string; // e.g. https://api.db.zaleit.com.au/v1
  /** ndb_ API key; omit for the unauthenticated endpoints (health, bootstrap). */
  apiKey?: string;
  fetchImpl?: typeof fetch;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public problem: {
      type?: string;
      title: string;
      status: number;
      detail?: string;
      request_id?: string;
    },
  ) {
    super(`${problem.title} (${status})`);
  }
}

export function createClient(opts: ClientOptions) {
  const f = opts.fetchImpl ?? fetch;

  async function request<T>(
    method: string,
    path: string,
    body?: unknown,
    init?: { idempotencyKey?: string },
  ): Promise<T> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };
    if (opts.apiKey) headers.Authorization = `Bearer ${opts.apiKey}`;
    if (init?.idempotencyKey) headers["Idempotency-Key"] = init.idempotencyKey;

    const res = await f(opts.baseUrl.replace(/\/$/, "") + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (res.status === 204) return undefined as T;
    const json = await res.json();
    if (!res.ok) throw new ApiError(res.status, json);
    return json as T;
  }

  type Op<P extends keyof paths, M extends keyof paths[P]> = paths[P][M];

  return {
    request,
    health: () =>
      request<{ status: string; version?: string }>("GET", "/healthz"),
    // Convenience wrappers for the Phase 1 surface; the generic `request`
    // covers everything else with `paths` types available for callers.
    listProjects: (params?: { limit?: number; cursor?: string }) => {
      const q = new URLSearchParams();
      if (params?.limit) q.set("limit", String(params.limit));
      if (params?.cursor) q.set("cursor", params.cursor);
      const qs = q.size ? `?${q}` : "";
      type R = Op<"/projects", "get">;
      return request<
        R extends { responses: { 200: { content: { "application/json": infer T } } } }
          ? T
          : never
      >("GET", `/projects${qs}`);
    },
  };
}
