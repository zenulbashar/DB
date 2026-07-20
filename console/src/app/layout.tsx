import type { Metadata } from "next";
import Link from "next/link";
import "./globals.css";
import { getToken } from "@/lib/session";
import { signOut } from "./connect/actions";

export const metadata: Metadata = {
  title: "NimbusDB Console",
  description: "Serverless PostgreSQL for the Nimbus platform",
};

export default async function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  const connected = Boolean(await getToken());
  return (
    <html lang="en">
      <body>
        <header className="border-b border-edge bg-forest">
          <div className="mx-auto flex h-14 max-w-6xl items-center gap-3 px-6">
            <Link href="/" className="font-semibold tracking-tight">
              Nimbus<span className="text-accent">DB</span>
            </Link>
            <span className="rounded-pill border border-forest-edge px-2 py-0.5 text-xs text-fg-muted">
              console
            </span>
            <div className="ml-auto flex items-center gap-4">
              <Link
                href="/kb"
                className="text-xs text-fg-muted transition-colors hover:text-fg"
              >
                Help
              </Link>
              {connected && (
                <form action={signOut}>
                  <button className="text-xs text-fg-muted transition-colors hover:text-fg">
                    Sign out
                  </button>
                </form>
              )}
            </div>
          </div>
        </header>
        <main className="mx-auto max-w-6xl px-6 py-10">{children}</main>
      </body>
    </html>
  );
}
