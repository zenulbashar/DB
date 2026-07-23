import Link from "next/link";
import { notFound } from "next/navigation";
import { allArticles, getArticle } from "@/lib/kb";

export function generateStaticParams() {
  return allArticles().map((a) => ({ slug: a.slug }));
}

export async function generateMetadata({
  params,
}: {
  params: Promise<{ slug: string }>;
}) {
  const article = getArticle((await params).slug);
  return article
    ? { title: `${article.title} — Zale DB KB`, description: article.summary }
    : {};
}

export default async function KBArticle({
  params,
}: {
  params: Promise<{ slug: string }>;
}) {
  const { slug } = await params;
  const article = getArticle(slug);
  if (!article) notFound();

  const siblings = allArticles().filter(
    (a) => a.category === article.category && a.slug !== slug,
  );

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div className="text-xs text-fg-muted">
        <Link href="/kb" className="transition-colors hover:text-fg">
          Knowledge Base
        </Link>{" "}
        / {article.category}
      </div>
      <article
        className="kb-prose"
        // Repo-authored markdown only (ADR-017) — never user content.
        dangerouslySetInnerHTML={{ __html: article.html }}
      />
      {siblings.length > 0 && (
        <aside className="rounded-card border border-edge bg-surface p-4">
          <div className="mb-2 text-xs font-semibold uppercase tracking-wide text-fg-muted">
            More in {article.category}
          </div>
          <ul className="space-y-1">
            {siblings.map((a) => (
              <li key={a.slug}>
                <Link
                  href={`/kb/${a.slug}`}
                  className="text-sm text-accent transition-colors hover:text-accent-hover"
                >
                  {a.title}
                </Link>
              </li>
            ))}
          </ul>
        </aside>
      )}
    </div>
  );
}
