# In-cluster manifests (Kustomize)

```
base/
  control-plane/     # API + reconciler Deployments, HPA, PDB   (Phase 2)
  pg-gateway/        # gateway Deployment + L4 Service           (Phase 2)
  operators/         # CNPG, cert-manager, external-secrets      (Phase 2)
  observability/     # Prometheus, Loki, Grafana, Alertmanager   (Phase 2)
overlays/
  staging/
  prod-syd1-a/
```

Applied exclusively by ArgoCD (`deploy/argocd/`). Manual cluster changes are
drift and get reverted. Image references are pinned by digest and bumped by CI.
