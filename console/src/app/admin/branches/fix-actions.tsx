"use client";

import { useState, useTransition } from "react";
import { Button, ErrorNote, Input } from "@/components/ui";
import {
  adminResizeBranch,
  adminResumeBranch,
  adminSuspendBranch,
  type AdminActionResult,
} from "../actions";

// Operator fix controls for one branch — the same state-gating as the tenant
// console's BranchActions, driving the audited admin endpoints instead.
export function FixActions({
  brID,
  state,
  minCu,
  maxCu,
  currentCu,
}: {
  brID: string;
  state: string;
  minCu: number;
  maxCu: number;
  currentCu: number;
}) {
  const [pending, start] = useTransition();
  const [error, setError] = useState<string>();
  const [showResize, setShowResize] = useState(false);
  const [cu, setCu] = useState<string>(String(currentCu || minCu));

  function run(fn: () => Promise<AdminActionResult>) {
    start(async () => {
      setError(undefined);
      const r = await fn();
      if (r?.error) setError(r.error);
      else setShowResize(false);
    });
  }

  return (
    <div className="flex flex-col items-end gap-2">
      <div className="flex items-center gap-2">
        {state === "ready" && (
          <>
            <Button
              size="sm"
              variant="secondary"
              loading={pending}
              onClick={() => setShowResize((v) => !v)}
            >
              Resize
            </Button>
            <Button
              size="sm"
              variant="ghost"
              loading={pending}
              onClick={() => run(() => adminSuspendBranch(brID))}
            >
              Suspend
            </Button>
          </>
        )}
        {state === "suspended" && (
          <Button
            size="sm"
            loading={pending}
            onClick={() => run(() => adminResumeBranch(brID))}
          >
            Resume
          </Button>
        )}
        {state === "error" && (
          <span className="text-xs text-fg-faint">
            no legal transition — investigate the cluster
          </span>
        )}
      </div>
      {showResize && state === "ready" && (
        <div className="flex items-center gap-2">
          <Input
            type="number"
            step="0.25"
            min={minCu}
            max={maxCu}
            value={cu}
            onChange={(e) => setCu(e.target.value)}
            className="h-7 w-24"
            aria-label="Compute units"
          />
          <Button size="sm" loading={pending} onClick={() => run(() => adminResizeBranch(brID, Number(cu)))}>
            Apply
          </Button>
        </div>
      )}
      {error && <ErrorNote>{error}</ErrorNote>}
    </div>
  );
}
