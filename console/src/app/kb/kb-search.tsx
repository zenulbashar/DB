"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import type { KBMeta } from "@/lib/kb";
import { Input } from "@/components/ui";

// Client-side filter over the article list the server page loaded — the KB is
// small enough that shipping the metadata and filtering locally beats a
// search backend by every measure that matters here.
export function KBSearch({
  articles,
  categories,
}: {
  articles: KBMeta[];
  categories: string[];
}) {
  const [q, setQ] = useState("");

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!needle) return articles;
    return articles.filter((a) =>
      `${a.title} ${a.summary} ${a.category}`.toLowerCase().includes(needle),
    );
  }, [articles, q]);

  const byCategory = useMemo(() => {
    const m = new Map<string, KBMeta[]>();
    for (const c of categories) m.set(c, []);
    for (const a of filtered) {
      if (!m.has(a.category)) m.set(a.category, []);
      m.get(a.category)!.push(a);
    }
    return [...m.entries()].filter(([, list]) => list.length > 0);
  }, [filtered, categories]);

  return (
    <div className="space-y-8">
      <Input
        type="search"
        placeholder="Search the knowledge base…"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        aria-label="Search articles"
        autoFocus
      />
      {byCategory.length === 0 && (
        <p className="text-sm text-fg-muted">
          No articles match “{q}”. Try a different term, or browse everything by
          clearing the search.
        </p>
      )}
      {byCategory.map(([category, list]) => (
        <section key={category}>
          <h2 className="mb-3 text-xs font-semibold uppercase tracking-wide text-fg-muted">
            {category}
          </h2>
          <div className="grid gap-3 sm:grid-cols-2">
            {list.map((a) => (
              <Link
                key={a.slug}
                href={`/kb/${a.slug}`}
                className="block rounded-card border border-edge bg-surface p-4 transition-colors hover:border-edge-strong"
              >
                <div className="text-sm font-medium">{a.title}</div>
                <p className="mt-1 text-xs text-fg-muted">{a.summary}</p>
              </Link>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}
