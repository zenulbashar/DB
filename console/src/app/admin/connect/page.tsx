"use client";

import { useActionState } from "react";
import { Button, Card } from "@/components/ui";
import { adminConnect, type AdminConnectState } from "./actions";

export default function AdminConnectPage() {
  const [state, action, pending] = useActionState<AdminConnectState, FormData>(
    adminConnect,
    {},
  );

  return (
    <div className="mx-auto max-w-md pt-10">
      <Card title="Operator sign-in">
        <p className="mb-4 text-sm text-fg-muted">
          Paste the platform operator token (<code className="text-fg">NDB_ADMIN_TOKEN</code>).
          This is not a tenant API key — it opens the cross-tenant operator
          surface only, and every fix action lands in the tenant&apos;s audit log.
        </p>
        <form action={action} className="space-y-3">
          <input
            name="token"
            type="password"
            autoComplete="off"
            placeholder="operator token"
            className="w-full rounded-control border border-edge-strong bg-surface px-3 py-2 font-mono text-sm outline-none focus:border-accent"
            aria-label="Operator token"
          />
          {state.error && (
            <p className="text-sm text-danger" role="alert">
              {state.error}
            </p>
          )}
          <Button type="submit" loading={pending}>
            {pending ? "Verifying…" : "Sign in"}
          </Button>
        </form>
      </Card>
    </div>
  );
}
