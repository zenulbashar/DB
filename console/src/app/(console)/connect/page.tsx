"use client";

import { useActionState } from "react";
import { Button, Card } from "@/components/ui";
import { connect, type ConnectState } from "./actions";

export default function ConnectPage() {
  const [state, action, pending] = useActionState<ConnectState, FormData>(connect, {});

  return (
    <div className="mx-auto max-w-md pt-10">
      <Card title="Connect to NimbusDB">
        <p className="mb-4 text-sm text-fg-muted">
          Paste an <code className="text-fg">ndb_</code> API key to sign in. Create one
          with the control-plane API or CLI; console sessions (email sign-in) arrive later.
        </p>
        <form action={action} className="space-y-3">
          <input
            name="token"
            type="password"
            autoComplete="off"
            placeholder="ndb_…"
            className="w-full rounded-control border border-edge-strong bg-surface px-3 py-2 font-mono text-sm outline-none focus:border-accent"
            aria-label="API key"
          />
          {state.error && (
            <p className="text-sm text-danger" role="alert">
              {state.error}
            </p>
          )}
          <Button type="submit" loading={pending}>
            {pending ? "Verifying…" : "Connect"}
          </Button>
        </form>
      </Card>
    </div>
  );
}
