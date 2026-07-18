# Deploy

Deployment assets per [DEPLOYMENT_ARCHITECTURE.md](../docs/architecture/DEPLOYMENT_ARCHITECTURE.md).

| Directory | Owns | Applied by |
|---|---|---|
| `terraform/` | Everything **below** the Kubernetes API: cluster, node pools, VPC, DNS, KMS, object storage (ADR-005: managed k8s in ap-southeast-2) | CI with manual approval on prod |
| `k8s/` | Everything **in** the cluster: platform components as Kustomize bases + per-env overlays | ArgoCD (never `kubectl apply` by hand) |
| `argocd/` | The app-of-apps that points ArgoCD at `k8s/` | Bootstrap script, once per cell |

Phase 1 ships skeletons; Phase 2 fills in the data-plane components (CNPG operator,
pg-gateway, cert-manager, Cilium policies, observability stack).

Secrets: SOPS(age)-encrypted only — plaintext secrets in this tree fail CI (gitleaks).
