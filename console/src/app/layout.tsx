import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "NimbusDB Console",
  description: "Serverless PostgreSQL for the Nimbus platform",
};

// Root layout is chrome-free: the tenant console shell lives in (console)/
// and the operator shell in admin/ — two apps, one token layer (ADR-018).
export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
