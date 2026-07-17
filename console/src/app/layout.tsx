import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "NimbusDB Console",
  description: "Serverless PostgreSQL for the Nimbus platform",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>
        <header className="border-b border-edge bg-forest">
          <div className="mx-auto flex h-14 max-w-6xl items-center gap-3 px-6">
            <span className="font-semibold tracking-tight">
              Nimbus<span className="text-accent">DB</span>
            </span>
            <span className="rounded-pill border border-forest-edge px-2 py-0.5 text-xs text-fg-muted">
              Phase 1 preview
            </span>
          </div>
        </header>
        <main className="mx-auto max-w-6xl px-6 py-10">{children}</main>
      </body>
    </html>
  );
}
