"use client";

import { useState, useTransition } from "react";
import { Button, ErrorNote, Input } from "@/components/ui";
import {
  resizeBranch,
  resumeBranch,
  suspendBranch,
  type ActionResult,
} from "./actions";

const TRANSITIONAL = new Set([
  "provisioning",
  "suspending",
  "resuming",
  "resizing",
  "deleting",
]);

// Lifecycle controls for one branch. Which buttons show is driven by the
// branch's current state — the same guard the control-plane state machine
// enforces (a 409 there still surfaces as an ErrorNote if the state raced).
export function BranchActions({
  prjId,
  brId,
  state,
  minCu,
  maxCu,
  currentCu,
}: {
  prjId: string;
  brId: string;
  state: string;
  minCu: number;
  maxCu: number;
  currentCu: number;
}) {
  const [pending, start] = useTransition();
  const [error, setError] = useState<string>();
  const [showResize, setShowResize] = useState(false);
  const [cu, setCu] = useState<string>(String(currentCu || minCu));

  function run(fn: () => Promise<ActionResult>) {
    start(async () => {
      setError(undefined);
      const r = await fn();
      if (r?.error) setError(r.error);
      else setShowResize(false);
    });
  }

  if (TRANSITIONAL.has(state)) {
    return <span className="text-xs text-fg-faint">working…</span>;
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
              onClick={() => run(() => suspendBranch(prjId, brId))}
            >
              Suspend
            </Button>
          </>
        )}
        {state === "suspended" && (
          <Button
            size="sm"
            loading={pending}
            onClick={() => run(() => resumeBranch(prjId, brId))}
          >
            Resume
          </Button>
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
          <span className="text-xs text-fg-faint">
            CU ({minCu}–{maxCu})
          </span>
          <Button
            size="sm"
            loading={pending}
            onClick={() => run(() => resizeBranch(prjId, brId, Number(cu)))}
          >
            Apply
          </Button>
        </div>
      )}
      {error && <ErrorNote>{error}</ErrorNote>}
    </div>
  );
}
