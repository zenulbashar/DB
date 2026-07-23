import Link from "next/link";
import { getAdminToken } from "@/lib/admin";
import { adminSignOut } from "./connect/actions";

// Operator shell (DESIGN_SYSTEM_MAPPING §4): same primitives as the tenant
// console, visually distinct chrome — neutral surface header instead of the
// forest brand, so an operator always knows which hat they're wearing.
export default async function AdminLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  const connected = Boolean(await getAdminToken());
  return (
    <>
      <header className="border-b border-edge-strong bg-surface-raised">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-3 px-6">
          <Link href="/admin" className="font-semibold tracking-tight">
            Zale<span className="text-accent">DB</span>
          </Link>
          <span className="rounded-pill border border-warning/50 px-2 py-0.5 text-xs text-warning">
            operator
          </span>
          {connected && (
            <nav className="ml-6 flex items-center gap-4 text-xs text-fg-muted">
              <Link href="/admin" className="transition-colors hover:text-fg">
                Overview
              </Link>
              <Link href="/admin/branches" className="transition-colors hover:text-fg">
                Branches
              </Link>
            </nav>
          )}
          <div className="ml-auto flex items-center gap-4">
            <Link
              href="/"
              className="text-xs text-fg-muted transition-colors hover:text-fg"
            >
              Tenant console
            </Link>
            {connected && (
              <form action={adminSignOut}>
                <button className="text-xs text-fg-muted transition-colors hover:text-fg">
                  Sign out
                </button>
              </form>
            )}
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-6 py-10">{children}</main>
    </>
  );
}
