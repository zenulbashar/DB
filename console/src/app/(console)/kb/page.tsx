import type { Metadata } from "next";
import { allArticles, CATEGORY_ORDER } from "@/lib/kb";
import { KBSearch } from "./kb-search";

export const metadata: Metadata = {
  title: "Knowledge Base — Zale DB",
  description: "Guides and reference for every Zale DB feature.",
};

// Public — help must be reachable when sign-in is the problem (ADR-017).
export default function KBIndex() {
  const articles = allArticles();
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Knowledge Base</h1>
        <p className="mt-1 text-sm text-fg-muted">
          Guides for every Zale DB feature — from first connection to
          production migrations.
        </p>
      </div>
      <KBSearch articles={articles} categories={CATEGORY_ORDER} />
    </div>
  );
}
