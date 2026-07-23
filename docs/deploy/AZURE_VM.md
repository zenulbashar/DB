# Deploying Zale DB on an Azure VM (self-host profile, ADR-020)

One Ubuntu VM running k3s carries the whole platform: control plane, console, pg-gateway,
reconciler, import worker, MinIO (WAL archives), and every tenant Postgres as a CNPG cluster.
Azure is used as a **dumb VM only** — no AKS, no Azure Database, no Blob, no Key Vault — which is
exactly what makes the later Binary Lane move cheap (see `BINARYLANE_MIGRATION.md`).

## 0. What you need

- An Azure subscription (the ~$230 credit covers roughly **2 months** on the recommended size).
- **A domain you control**, e.g. `zaleit.com.au`. The platform will use:
  - `api.db.zaleit.com.au`, `console.db.zaleit.com.au`, `hosting.db.zaleit.com.au` — HTTPS via Traefik
  - `*.syd1.db.zaleit.com.au` — tenant database endpoints (Postgres TLS/SNI via pg-gateway)
- Recommended: move the domain's DNS to **Cloudflare (free)** — the wildcard certificate needs a
  DNS-01 challenge, and the bootstrap script has first-class Cloudflare support. Create an API
  token with `Zone → DNS → Edit` on your zone.

## 1. Create the VM (az CLI; the portal works identically)

```bash
az group create --name nimbusdb-rg --location australiaeast

az vm create \
  --resource-group nimbusdb-rg \
  --name nimbusdb-1 \
  --image Ubuntu2404 \
  --size Standard_B4as_v2 \
  --os-disk-size-gb 128 \
  --public-ip-sku Standard \
  --admin-username ndb \
  --generate-ssh-keys

# Static IP (survives VM restarts; you'll point DNS at it)
az network public-ip update --resource-group nimbusdb-rg \
  --name nimbusdb-1PublicIP --allocation-method Static

# Firewall: SSH from your IP only; 80/443 (console/API/ACME) + 5432 (Postgres) to the world
MYIP=$(curl -s https://ifconfig.me)
az network nsg rule create -g nimbusdb-rg --nsg-name nimbusdb-1NSG -n ssh    --priority 1000 --source-address-prefixes "$MYIP" --destination-port-ranges 22  --access Allow --protocol Tcp
az network nsg rule create -g nimbusdb-rg --nsg-name nimbusdb-1NSG -n http   --priority 1010 --destination-port-ranges 80  --access Allow --protocol Tcp
az network nsg rule create -g nimbusdb-rg --nsg-name nimbusdb-1NSG -n https  --priority 1020 --destination-port-ranges 443 --access Allow --protocol Tcp
az network nsg rule create -g nimbusdb-rg --nsg-name nimbusdb-1NSG -n pgsql  --priority 1030 --destination-port-ranges 5432 --access Allow --protocol Tcp
```

**Sizing / cost** (australiaeast, pay-as-you-go, subject to change):

| Item | Choice | ~Cost |
|---|---|---|
| VM | `Standard_B4as_v2` (4 vCPU / 16 GiB, burstable AMD) | ~US$115/mo |
| Budget option | `Standard_B2as_v2` (2 vCPU / 8 GiB) — fine until real tenant load | ~US$58/mo |
| OS disk | 128 GiB Premium SSD | ~US$20/mo |
| Static IP | Standard | ~US$4/mo |

`B4as_v2` gives headroom for the platform (~2 CPU / 4 GiB) plus several tenant branches.

## 2. DNS records (do this early — TTL matters later)

Point these **A records** at the VM's static IP, **TTL 300**:

```
api.db.zaleit.com.au        A  <VM_IP>
console.db.zaleit.com.au    A  <VM_IP>
hosting.db.zaleit.com.au    A  <VM_IP>
*.syd1.db.zaleit.com.au     A  <VM_IP>
```

The low TTL is deliberate: the Binary Lane migration later is "flip these records" — a 5-minute
TTL makes that cutover a 5-minute event.

## 3. Bootstrap the platform

```bash
ssh ndb@<VM_IP>
sudo -i
git clone https://github.com/zenulbashar/DB.git /opt/nimbusdb && cd /opt/nimbusdb

deploy/vm/gen-secrets.sh          # writes /etc/nimbusdb/secrets.env
vi /etc/nimbusdb/secrets.env      # set NDB_DOMAIN=db.zaleit.com.au and CLOUDFLARE_API_TOKEN=<token>

deploy/vm/bootstrap.sh
```

The script is idempotent (safe to re-run): installs k3s, cert-manager, the CNPG operator; creates
all secrets; applies `deploy/k8s/overlays/selfhost`; renders the domain-dependent Issuer,
wildcard Certificate, and Ingresses; waits and prints next steps.

> **Back up `/etc/nimbusdb/secrets.env` somewhere safe now.** It holds the KEKs and tokens;
> reusing it on a replacement VM is what makes migration a non-event.

## 4. First-run verification

```bash
# certs Ready (wildcard can take a minute or two)
kubectl get certificate -A

# 1. platform init — save the returned api_key.token, it is shown exactly once
curl -X POST https://api.db.zaleit.com.au/v1/bootstrap -H 'Content-Type: application/json' \
  -d '{"bootstrap_token":"<NDB_BOOTSTRAP_TOKEN>","email":"you@example.com","org_name":"YourOrg"}'

# 2. console: https://console.db.zaleit.com.au  → sign in with the zdb_ key → New project
#    operator console: https://console.db.zaleit.com.au/admin  → sign in with NDB_ADMIN_TOKEN

# 3. watch the branch reach ready (reconciler → CNPG on k3s)
kubectl get clusters -A -w

# 4. connect through the gateway (TLS + SNI, end to end)
psql "postgresql://<role>:<password>@ep-…​.syd1.db.zaleit.com.au:5432/<db>?sslmode=require"

# 5. scale-to-zero: suspend the branch in the console, reconnect — the first
#    connection wakes it (allow ~30 s connect timeout).
```

The in-console Knowledge Base (`https://console.db.zaleit.com.au/kb`) covers everything user-facing.

## 5. The hosting app (optional, separate repo)

`deploy/k8s/base/hosting-app.yaml` ships scaled to 0 with an image placeholder. Build the Nimbus
hosting app's image from its own repo (push to GHCR, or `docker build` + `k3s ctr images import`
on the VM), set the image, then:

```bash
kubectl -n nimbusdb-platform set image deploy/nimbus-hosting hosting=<your-image>
kubectl -n nimbusdb-platform scale deploy/nimbus-hosting --replicas=1
# → https://hosting.db.zaleit.com.au
```

## 6. Day-2 basics

- **Upgrades:** `git -C /opt/nimbusdb pull && deploy/vm/bootstrap.sh` re-applies manifests;
  `kubectl rollout restart deploy -n nimbusdb-platform` pulls fresh `:latest` images.
- **Backups are on by default** — every branch archives WAL to MinIO; the restore-verification
  loop (`NDB_VERIFY_INTERVAL=24h`) proves archives restorable and pages via the admin console.
- **Honest caveat (ADR-020):** one VM is one failure domain. The WAL archive is the durability
  net; for whole-VM loss you rebuild via `bootstrap.sh` + restore (same procedure as the planned
  migration — which doubles as your DR drill).
- **When the Azure credit runs out:** you were always going to move — see
  `BINARYLANE_MIGRATION.md`; budget one evening.
