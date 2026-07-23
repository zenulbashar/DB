# Substrate bootstrap for cell syd1-a (ADR-005: managed Kubernetes +
# cloud object storage/KMS in ap-southeast-2).
#
# Phase 1 intentionally ships structure, not resources: the concrete provider
# module (EKS vs AKS vs GKE) is selected at Phase 2 bootstrap on quota/pricing,
# and everything above the k8s API is provider-agnostic by design.
#
# Hard requirements for any provider module (DEPLOYMENT_ARCHITECTURE §1):
#   - CSI driver with VolumeSnapshot + expansion support
#   - >= 2 availability zones
#   - S3-compatible object storage in-region
#   - KMS for envelope-encryption root keys
#   - L4 load balancer with a stable IP for pg-gateway

terraform {
  required_version = ">= 1.9"

  # State backend is configured per environment at `terraform init` time:
  #   terraform init -backend-config=envs/<env>.backend.hcl
  backend "s3" {}
}

variable "region" {
  description = "Cell region (cloud-provider region for ap-southeast-2 / syd1)"
  type        = string
}

variable "cell" {
  description = "Cell identifier, e.g. syd1-a (MULTI_TENANCY §6)"
  type        = string
}

# module "cluster"        { ... }  # Phase 2: managed k8s + node pools (system/controlplane/gateway/tenant)
# module "object_storage" { ... }  # Phase 2: WAL archive + backup buckets, second-region replica bucket
# module "kms"            { ... }  # Phase 2: envelope-encryption root key
# module "dns"            { ... }  # Phase 2: *.syd1.db.zaleit.com.au, api/console records
