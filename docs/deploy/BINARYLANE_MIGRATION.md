# Migrating from the Azure VM to a Binary Lane VPS (ADR-020)

Everything platform-shaped lives inside k3s and MinIO, and the images live in GHCR — so the move
is: **provision → same bootstrap → sync data → flip DNS**. Budget one evening; actual downtime is
minutes (bounded by your DNS TTL of 300 s).

## 0. Provision the VPS

Binary Lane, **Sydney** region, Ubuntu 24.04:

- Match the Azure size or better: 4 vCPU / 16 GB / ≥120 GB NVMe (~AU$50–60/mo).
- Firewall (Binary Lane "Advanced networking" or ufw on the VM): 22 (your IP), 80, 443, 5432.

## 1. Bootstrap with the SAME secrets

```bash
# on the Binary Lane VPS, as root
git clone https://github.com/zenulbashar/DB.git /opt/nimbusdb && cd /opt/nimbusdb
mkdir -p /etc/nimbusdb
scp azure-vm:/etc/nimbusdb/secrets.env /etc/nimbusdb/secrets.env   # ← the whole trick
chmod 600 /etc/nimbusdb/secrets.env
deploy/vm/bootstrap.sh
```

Same `secrets.env` ⇒ same KEKs (every stored secret decrypts), same tokens (every client keeps
working), same domain. Nothing is reconfigured. The platform comes up **empty** — data arrives next.

## 2. Sync the archives (MinIO → MinIO)

Run from either machine (`mc` = MinIO client). First pass with both platforms running:

```bash
mc alias set az  http://<AZURE_VM_IP>:9000  "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"
mc alias set bl  http://<BL_VPS_IP>:9000    "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"
mc mirror --preserve az/ndb-wal bl/ndb-wal        # bulk copy while Azure still serves
```

(Expose 9000 temporarily to the other VM's IP only, or tunnel it over SSH:
`ssh -L 9000:minio.minio.svc:9000 …` via `kubectl port-forward -n minio svc/minio 9000:9000`.)

## 3. Cutover (the brief write-freeze)

1. **Quiesce Azure:** suspend tenant branches from the operator console (`/admin/branches` →
   Suspend) — suspended branches have their WAL fully archived; then scale the control plane
   writers down: `kubectl -n nimbusdb-platform scale deploy ndb-api ndb-reconciler ndb-import-worker --replicas=0`.
2. **Final backup + sync:** on Azure, take a last control-plane base backup
   (`kubectl -n nimbusdb-platform create -f - <<< '{"apiVersion":"postgresql.cnpg.io/v1","kind":"Backup","metadata":{"name":"final","namespace":"nimbusdb-platform"},"spec":{"cluster":{"name":"ndb-controlplane"}}}'`),
   wait for it to complete, then re-run `mc mirror --preserve az/ndb-wal bl/ndb-wal` (delta only).
3. **Restore the control-plane DB on Binary Lane:** edit
   `deploy/k8s/base/controlplane-db.yaml` on the VPS clone to bootstrap by recovery instead of
   initdb (one-time):
   ```yaml
   bootstrap:
     recovery: { source: origin }
   externalClusters:
     - name: origin
       barmanObjectStore:
         destinationPath: s3://ndb-wal/controlplane
         endpointURL: http://minio.minio.svc:9000
         s3Credentials:
           accessKeyId: { name: ndb-backup-credentials, key: ACCESS_KEY_ID }
           secretAccessKey: { name: ndb-backup-credentials, key: ACCESS_SECRET_KEY }
   ```
   `kubectl delete cluster ndb-controlplane -n nimbusdb-platform && kubectl apply -k deploy/k8s/overlays/selfhost`
   — CNPG restores the control-plane DB (orgs, projects, branches, keys, encrypted secrets — the
   same KEKs decrypt them).
4. **Tenant branches restore themselves:** with the control-plane DB back, resume each branch
   (operator console → Resume). Branch clusters that don't exist yet on the new node re-provision;
   their data comes back **from the mirrored WAL archives** via the same recovery path branching
   and restore-verification already use. Verify each with a `psql` through the gateway.
5. **Flip DNS:** change the four A records (`api.`, `console.`, `hosting.`, `*.syd1.`) to the
   Binary Lane IP. TTL 300 ⇒ clients land on the new VM within minutes. cert-manager re-issues
   certs on the new machine automatically (same domain, same issuer).
6. **Verify, then decommission Azure:** run through §4 of `AZURE_VM.md` against the new IP; keep
   the Azure VM stopped (not deleted) for a few days as rollback, then
   `az group delete --name nimbusdb-rg`.

## Rollback at any point before step 5

Azure is untouched until the DNS flip: scale its deployments back up and you're exactly where you
started. After the flip, flipping DNS back is equally valid as long as you haven't written new
data on Binary Lane you care about.

## Why this is "minimum admin effort"

| Concern | Answer |
|---|---|
| Reconfiguration | None — same `secrets.env`, same manifests, same images (GHCR digests) |
| Data | One `mc mirror` + one CNPG recovery bootstrap |
| Clients | Same domain, same connection strings; DNS TTL 300 bounds the blip |
| Certs | cert-manager re-issues automatically on the new VM |
| Rehearsal | This procedure ≡ the DR runbook — doing the migration *is* the restore drill (R-2) |
