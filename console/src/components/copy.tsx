"use client";

import { useState } from "react";

// CopyField shows a monospace value with a one-click copy button. Used for
// connection strings and endpoint hosts. The value is masked-at-source by the
// caller for secret-bearing strings (the console never fetches a password
// unless the user asks — the audited reveal flow).
export function CopyField({ label, value }: { label?: string; value: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch {
      /* clipboard blocked (e.g. insecure context) — no-op */
    }
  }
  return (
    <div>
      {label && <div className="mb-1 text-xs font-medium text-fg-muted">{label}</div>}
      <div className="flex items-stretch gap-2">
        <code className="block flex-1 overflow-x-auto rounded-control border border-edge bg-background px-3 py-2 font-mono text-xs text-fg-muted">
          {value}
        </code>
        <button
          type="button"
          onClick={copy}
          className="shrink-0 rounded-control border border-edge-strong px-3 text-xs text-fg-muted transition-colors hover:border-fg-muted hover:text-fg"
          aria-label={`Copy ${label ?? "value"}`}
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
    </div>
  );
}
