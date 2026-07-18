import {
  Badge,
  Button,
  Card,
  ConnectionString,
  StatusDot,
} from "@/components/ui";

/*
 * Phase 1 shell: static preview of the design system against the token layer.
 * Live data (projects, metrics, SQL editor) arrives with console auth in
 * Phase 3 (ROADMAP.md).
 */
export default function Home() {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Projects</h1>
        <p className="mt-1 text-sm text-fg-muted">
          Serverless PostgreSQL in <Badge>syd1</Badge> — control plane API is
          live; provisioning lands in Phase 2.
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Card title="prompt2eat">
          <div className="flex items-center gap-2 text-sm text-fg-muted">
            <StatusDot state="provisioning" /> pending · PG 17
          </div>
          <div className="mt-4">
            <ConnectionString value="postgresql://app:secret@ep-example.syd1.db.nimbus.app/prompt2eat?sslmode=require" />
          </div>
        </Card>
        <Card title="roster">
          <div className="flex items-center gap-2 text-sm text-fg-muted">
            <StatusDot state="ready" /> ready (preview) · PG 17
          </div>
          <div className="mt-4">
            <ConnectionString value="postgresql://app:secret@ep-sample.syd1.db.nimbus.app/roster?sslmode=require" />
          </div>
        </Card>
      </div>

      <div className="flex gap-3">
        <Button>New project</Button>
        <Button variant="secondary">Import database</Button>
        <Button variant="ghost">Documentation</Button>
      </div>
    </div>
  );
}
