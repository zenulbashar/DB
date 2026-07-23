#!/usr/bin/env bash
# Zale DB self-host bootstrap (ADR-020). Idempotent; identical on the Azure
# VM and the Binary Lane VPS — the provider only supplies the VM, firewall,
# and DNS. Run as root from a clone of this repo:
#
#   deploy/vm/gen-secrets.sh            # once; edit NDB_DOMAIN (+ CF token)
#   deploy/vm/bootstrap.sh
#
# Requires: Ubuntu 22.04/24.04, ports 80/443/5432 reachable, DNS A records
# for api.<domain>, console.<domain>, hosting.<domain> and *.$NDB_REGION.<domain>
# pointing at this VM (TTL 300 recommended — it makes the later migration cheap).
set -euo pipefail

SECRETS="${SECRETS_FILE:-/etc/nimbusdb/secrets.env}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GENERATED=/etc/nimbusdb/generated

K3S_CHANNEL="${K3S_CHANNEL:-v1.31}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.16.3}"
CNPG_VERSION="${CNPG_VERSION:-1.25.1}"

say() { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mBOOTSTRAP FAIL: %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || fail "run as root"
[ -f "$SECRETS" ] || fail "$SECRETS missing — run deploy/vm/gen-secrets.sh first"
# shellcheck disable=SC1090
source "$SECRETS"
[ -n "${NDB_DOMAIN:-}" ] && [ "$NDB_DOMAIN" != "CHANGE-ME.example.com" ] || fail "set NDB_DOMAIN in $SECRETS"
NDB_REGION="${NDB_REGION:-syd1}"

say "1/6 k3s (channel $K3S_CHANNEL)"
if ! command -v kubectl >/dev/null 2>&1 || ! systemctl is-active --quiet k3s; then
  curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL="$K3S_CHANNEL" sh -
fi
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl wait --for=condition=Ready node --all --timeout=120s

say "2/6 cert-manager $CERT_MANAGER_VERSION + CNPG $CNPG_VERSION"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
kubectl apply --server-side -f \
  "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-${CNPG_VERSION%.*}/releases/cnpg-${CNPG_VERSION}.yaml"
kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s
kubectl -n cnpg-system rollout status deploy/cnpg-controller-manager --timeout=180s

say "3/6 secrets"
apply_secret() { kubectl create secret generic "$@" --dry-run=client -o yaml | kubectl apply -f -; }
kubectl create namespace nimbusdb-platform --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace nimbusdb-gateway --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace minio --dry-run=client -o yaml | kubectl apply -f -

apply_secret minio-root -n minio \
  --from-literal=MINIO_ROOT_USER="$MINIO_ROOT_USER" \
  --from-literal=MINIO_ROOT_PASSWORD="$MINIO_ROOT_PASSWORD"
# Canonical archive credentials — the reconciler replicates this into every
# tenant namespace (ADR-020).
apply_secret ndb-backup-credentials -n nimbusdb-platform \
  --from-literal=ACCESS_KEY_ID="$MINIO_ROOT_USER" \
  --from-literal=ACCESS_SECRET_KEY="$MINIO_ROOT_PASSWORD"
kubectl create secret generic ndb-app-db-credentials -n nimbusdb-platform \
  --type=kubernetes.io/basic-auth \
  --from-literal=username=ndb_app \
  --from-literal=password="$NDB_APP_DB_PASSWORD" \
  --dry-run=client -o yaml | kubectl apply -f -

DATABASE_URL="postgres://ndb_app:${NDB_APP_DB_PASSWORD}@ndb-controlplane-rw.nimbusdb-platform.svc:5432/nimbusdb_cp?sslmode=require"
apply_secret ndb-api-env -n nimbusdb-platform \
  --from-literal=DATABASE_URL="$DATABASE_URL" \
  --from-literal=NDB_BOOTSTRAP_TOKEN="$NDB_BOOTSTRAP_TOKEN" \
  --from-literal=NDB_GATEWAY_TOKEN="$NDB_GATEWAY_TOKEN" \
  --from-literal=NDB_ADMIN_TOKEN="$NDB_ADMIN_TOKEN" \
  --from-literal=NDB_KEKS="$NDB_KEKS" \
  --from-literal=NDB_ACTIVE_KEK="$NDB_ACTIVE_KEK" \
  --from-literal=NDB_ENV=prod \
  --from-literal=NDB_DOMAIN="$NDB_DOMAIN"
apply_secret ndb-reconciler-env -n nimbusdb-platform \
  --from-literal=DATABASE_URL="$DATABASE_URL" \
  --from-literal=NDB_KEKS="$NDB_KEKS" \
  --from-literal=NDB_ACTIVE_KEK="$NDB_ACTIVE_KEK" \
  --from-literal=NDB_ENV=prod \
  --from-literal=NDB_DOMAIN="$NDB_DOMAIN" \
  --from-literal=NDB_BACKUP_BUCKET="s3://ndb-wal" \
  --from-literal=NDB_BACKUP_ENDPOINT_URL="http://minio.minio.svc:9000" \
  --from-literal=NDB_BACKUP_CREDENTIALS_SECRET=ndb-backup-credentials \
  --from-literal=NDB_BACKUP_CREDENTIALS_NAMESPACE=nimbusdb-platform \
  --from-literal=NDB_VERIFY_INTERVAL=24h
apply_secret ndb-import-worker-env -n nimbusdb-platform \
  --from-literal=DATABASE_URL="$DATABASE_URL" \
  --from-literal=NDB_KEKS="$NDB_KEKS" \
  --from-literal=NDB_ACTIVE_KEK="$NDB_ACTIVE_KEK" \
  --from-literal=NDB_ENV=prod
apply_secret ndb-console-env -n nimbusdb-platform \
  --from-literal=NDB_API_URL="https://api.${NDB_DOMAIN}/v1"
apply_secret ndb-gateway-env -n nimbusdb-gateway \
  --from-literal=PGGW_GATEWAY_TOKEN="$NDB_GATEWAY_TOKEN"
if [ -n "${CLOUDFLARE_API_TOKEN:-}" ]; then
  apply_secret cloudflare-api-token -n cert-manager \
    --from-literal=api-token="$CLOUDFLARE_API_TOKEN"
fi

say "4/6 render domain-dependent resources → $GENERATED"
mkdir -p "$GENERATED"
if [ -n "${CLOUDFLARE_API_TOKEN:-}" ]; then
  cat > "$GENERATED/issuer.yaml" <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef: { name: letsencrypt-account }
    solvers:
      - dns01:
          cloudflare:
            apiTokenSecretRef: { name: cloudflare-api-token, key: api-token }
EOF
else
  cat > "$GENERATED/issuer.yaml" <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef: { name: letsencrypt-account }
    solvers:
      - http01:
          ingress: { class: traefik }
EOF
  echo "WARNING: no CLOUDFLARE_API_TOKEN — HTTP-01 covers api/console/hosting, but the"
  echo "         gateway WILDCARD cert (*.${NDB_REGION}.${NDB_DOMAIN}) needs DNS-01."
  echo "         Provide the wildcard cert yourself as secret pg-gateway-tls in"
  echo "         namespace nimbusdb-gateway, or add a DNS-01 solver."
fi
cat > "$GENERATED/gateway-cert.yaml" <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: pg-gateway-tls
  namespace: nimbusdb-gateway
spec:
  secretName: pg-gateway-tls
  issuerRef: { name: letsencrypt, kind: ClusterIssuer }
  dnsNames: ["*.${NDB_REGION}.${NDB_DOMAIN}"]
EOF
for app in api:ndb-api:8080:nimbusdb-platform console:ndb-console:3000:nimbusdb-platform hosting:nimbus-hosting:3000:nimbusdb-platform; do
  IFS=: read -r host svc port ns <<< "$app"
  cat > "$GENERATED/ingress-$host.yaml" <<EOF
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: $host
  namespace: $ns
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt
spec:
  ingressClassName: traefik
  tls:
    - hosts: ["$host.${NDB_DOMAIN}"]
      secretName: $host-tls
  rules:
    - host: $host.${NDB_DOMAIN}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: $svc
                port: { number: $port }
EOF
done

say "5/6 apply platform"
kubectl apply -k "$REPO_ROOT/deploy/k8s/overlays/selfhost"
kubectl apply -f "$GENERATED"

say "6/6 wait + summary"
kubectl -n nimbusdb-platform rollout status deploy/ndb-api --timeout=300s || true
kubectl -n nimbusdb-platform rollout status deploy/ndb-reconciler --timeout=300s || true
kubectl -n nimbusdb-platform rollout status deploy/ndb-console --timeout=300s || true
kubectl -n nimbusdb-gateway rollout status deploy/pg-gateway --timeout=300s || true
kubectl get cluster -n nimbusdb-platform 2>/dev/null || true
kubectl get certificate -A 2>/dev/null || true

cat <<EOF

Bootstrap applied. Next:
  1. Watch certs:      kubectl get certificate -A          (Ready=True)
  2. Platform init:    curl -X POST https://api.${NDB_DOMAIN}/v1/bootstrap \\
                         -H 'Content-Type: application/json' \\
                         -d '{"bootstrap_token":"<NDB_BOOTSTRAP_TOKEN from $SECRETS>","email":"you@example.com","org_name":"YourOrg"}'
     → SAVE the returned api_key.token (shown exactly once).
  3. Console:          https://console.${NDB_DOMAIN}   (sign in with that key)
     Operator console: https://console.${NDB_DOMAIN}/admin  (NDB_ADMIN_TOKEN)
  4. Create a project in the console, then connect:
     psql "postgresql://<role>:<password>@ep-….${NDB_REGION}.${NDB_DOMAIN}:5432/<db>?sslmode=require"
EOF
