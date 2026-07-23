#!/usr/bin/env bash
# Generate /etc/nimbusdb/secrets.env — every secret the platform needs, once.
# KEEP THIS FILE: reusing it on the Binary Lane VPS is what makes the
# migration a non-event (same KEKs decrypt everything; same tokens keep every
# client working). ADR-020.
set -euo pipefail

OUT="${1:-/etc/nimbusdb/secrets.env}"
if [ -f "$OUT" ]; then
  echo "$OUT already exists — refusing to overwrite (this file is the platform's keys)." >&2
  exit 1
fi
mkdir -p "$(dirname "$OUT")"

cat > "$OUT" <<EOF
# NimbusDB self-host secrets (generated $(date -u +%FT%TZ)). chmod 600, back it
# up somewhere safe, and REUSE IT on any replacement VM (ADR-020).

# --- you fill these two in ---
NDB_DOMAIN=CHANGE-ME.example.com      # your platform domain (console.<d>, api.<d>, *.syd1.<d>)
CLOUDFLARE_API_TOKEN=                 # DNS-01 token for the wildcard cert (Zone:DNS:Edit). Leave
                                      # empty to handle the wildcard cert another way.

NDB_REGION=syd1

# --- generated; do not change after first boot ---
NDB_KEKS=1:$(openssl rand -base64 32)
NDB_ACTIVE_KEK=1
NDB_BOOTSTRAP_TOKEN=$(openssl rand -hex 24)
NDB_GATEWAY_TOKEN=$(openssl rand -hex 24)
NDB_ADMIN_TOKEN=$(openssl rand -hex 24)
NDB_APP_DB_PASSWORD=$(openssl rand -hex 24)
MINIO_ROOT_USER=ndbminio
MINIO_ROOT_PASSWORD=$(openssl rand -hex 24)
EOF
chmod 600 "$OUT"
echo "wrote $OUT"
echo "EDIT IT NOW: set NDB_DOMAIN (and CLOUDFLARE_API_TOKEN for the wildcard cert), then run bootstrap.sh"
