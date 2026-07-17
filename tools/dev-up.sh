#!/usr/bin/env bash
# Bootstrap a local kind cluster with the data-plane operators installed.
# Phase 2 fills this in with CNPG Cluster fixtures; Phase 1 only needs the substrate.
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-nimbusdb-dev}"
CNPG_VERSION="${CNPG_VERSION:-1.25.1}"

command -v kind >/dev/null || { echo "kind is required: https://kind.sigs.k8s.io"; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl is required"; exit 1; }

if ! kind get clusters | grep -qx "$CLUSTER_NAME"; then
  kind create cluster --name "$CLUSTER_NAME"
fi

# CloudNativePG operator
kubectl apply --server-side -f \
  "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-${CNPG_VERSION%.*}/releases/cnpg-${CNPG_VERSION}.yaml"

kubectl -n cnpg-system rollout status deployment/cnpg-controller-manager --timeout=180s

echo "kind cluster '$CLUSTER_NAME' ready with CNPG ${CNPG_VERSION}."
echo "Next: docker compose up -d postgres && make migrate && make dev"
