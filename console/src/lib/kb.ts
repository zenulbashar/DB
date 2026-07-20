import fs from "node:fs";
import path from "node:path";
import { marked } from "marked";

// Knowledge-base loader (ADR-017): articles are repo-authored markdown under
// console/content/kb with a small frontmatter header. Server-only — pages call
// these at render time. Content is trusted (ships with the repo); never route
// user-generated markdown through here.

export type KBMeta = {
  slug: string;
  title: string;
  category: string;
  order: number;
  summary: string;
};

const KB_DIR = path.join(process.cwd(), "content", "kb");

// Categories in display order; anything unknown sorts last alphabetically.
export const CATEGORY_ORDER = [
  "Getting started",
  "Projects & branches",
  "Connecting",
  "Compute & scaling",
  "Data safety",
  "Imports & migration",
  "Access & security",
  "Reference",
];

function parseFrontmatter(raw: string): { meta: Record<string, string>; body: string } {
  const m = raw.match(/^---\n([\s\S]*?)\n---\n/);
  if (!m) return { meta: {}, body: raw };
  const meta: Record<string, string> = {};
  for (const line of m[1].split("\n")) {
    const idx = line.indexOf(":");
    if (idx > 0) meta[line.slice(0, idx).trim()] = line.slice(idx + 1).trim();
  }
  return { meta, body: raw.slice(m[0].length) };
}

function readMeta(slug: string, raw: string): KBMeta {
  const { meta } = parseFrontmatter(raw);
  return {
    slug,
    title: meta.title ?? slug,
    category: meta.category ?? "Reference",
    order: Number(meta.order ?? 99),
    summary: meta.summary ?? "",
  };
}

export function allArticles(): KBMeta[] {
  const files = fs.readdirSync(KB_DIR).filter((f) => f.endsWith(".md"));
  const metas = files.map((f) =>
    readMeta(f.replace(/\.md$/, ""), fs.readFileSync(path.join(KB_DIR, f), "utf8")),
  );
  const catRank = (c: string) => {
    const i = CATEGORY_ORDER.indexOf(c);
    return i === -1 ? CATEGORY_ORDER.length : i;
  };
  return metas.sort(
    (a, b) =>
      catRank(a.category) - catRank(b.category) ||
      a.order - b.order ||
      a.title.localeCompare(b.title),
  );
}

export function getArticle(slug: string): (KBMeta & { html: string }) | null {
  // Defense in depth: slugs are filenames; never let a path escape KB_DIR.
  if (!/^[a-z0-9-]+$/.test(slug)) return null;
  const file = path.join(KB_DIR, `${slug}.md`);
  if (!fs.existsSync(file)) return null;
  const raw = fs.readFileSync(file, "utf8");
  const { body } = parseFrontmatter(raw);
  return { ...readMeta(slug, raw), html: marked.parse(body) as string };
}
