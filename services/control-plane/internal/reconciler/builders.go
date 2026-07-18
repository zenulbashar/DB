// Package reconciler converges control-plane desired state into Kubernetes
// objects (SYSTEM_ARCHITECTURE §3.1): per-project namespaces with quotas and
// default-deny network policies, one CNPG Cluster + Pooler per branch, and
// the gateway route-table ConfigMap.
//
// Builders in this file are pure: BranchWork in, objects out — the unit-test
// surface for everything the reconciler creates.
package reconciler

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

var (
	ClusterGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	PoolerGVK  = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Pooler"}
)

const (
	// GatewayNamespace hosts pg-gateway and receives the route ConfigMap.
	GatewayNamespace = "nimbusdb-gateway"
	RoutesConfigMap  = "pg-gateway-routes"

	labelManagedBy = "app.kubernetes.io/managed-by"
	managerName    = "nimbusdb-reconciler"
	labelProject   = "nimbusdb.io/project"
	labelBranch    = "nimbusdb.io/branch"
	labelOrg       = "nimbusdb.io/org"
)

// K8s object names: ULID-based IDs are already lowercase; swap the type
// separator for a DNS-safe dash. prj_01x → prj-01x, br_01y → br-01y.
func NamespaceName(projectID string) string { return strings.ReplaceAll(projectID, "_", "-") }
func ClusterName(branchID string) string    { return strings.ReplaceAll(branchID, "_", "-") }
func PoolerName(branchID string) string     { return ClusterName(branchID) + "-pooler" }

// BackendFor derives the in-cluster service address the gateway dials for an
// endpoint (DATABASE_ARCHITECTURE §5 wiring).
func BackendFor(kind domain.EndpointKind, branchID, projectID string) string {
	ns := NamespaceName(projectID)
	switch kind {
	case domain.EndpointRWPooled:
		return fmt.Sprintf("%s.%s.svc:5432", PoolerName(branchID), ns)
	case domain.EndpointROPooled:
		return fmt.Sprintf("%s-ro.%s.svc:5432", ClusterName(branchID), ns)
	default: // rw_direct
		return fmt.Sprintf("%s-rw.%s.svc:5432", ClusterName(branchID), ns)
	}
}

func commonLabels(w postgres.BranchWork) map[string]any {
	return map[string]any{
		labelManagedBy: managerName,
		labelProject:   w.ProjectID,
		labelBranch:    w.Branch.ID,
		labelOrg:       w.OrgID,
	}
}

func BuildNamespace(w postgres.BranchWork) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name": NamespaceName(w.ProjectID),
			"labels": map[string]any{
				labelManagedBy: managerName,
				labelProject:   w.ProjectID,
				labelOrg:       w.OrgID,
			},
		},
	}}
	return u
}

// BuildResourceQuota bounds a project namespace (MULTI_TENANCY §3). Static
// generous defaults in v1; plan-derived values arrive with billing (Phase 7).
func BuildResourceQuota(w postgres.BranchWork) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ResourceQuota",
		"metadata": map[string]any{
			"name":      "project-quota",
			"namespace": NamespaceName(w.ProjectID),
			"labels":    map[string]any{labelManagedBy: managerName, labelProject: w.ProjectID},
		},
		"spec": map[string]any{
			"hard": map[string]any{
				"pods":                   "30",
				"requests.cpu":           "24",
				"requests.memory":        "96Gi",
				"persistentvolumeclaims": "20",
			},
		},
	}}
}

// BuildNetworkPolicies returns the namespace's default-deny ingress policy
// plus the single allow: gateway pods → Postgres/pooler port (MULTI_TENANCY §2).
func BuildNetworkPolicies(w postgres.BranchWork) []*unstructured.Unstructured {
	ns := NamespaceName(w.ProjectID)
	deny := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata": map[string]any{
			"name": "default-deny-ingress", "namespace": ns,
			"labels": map[string]any{labelManagedBy: managerName},
		},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"policyTypes": []any{"Ingress"},
		},
	}}
	allowGateway := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata": map[string]any{
			"name": "allow-gateway", "namespace": ns,
			"labels": map[string]any{labelManagedBy: managerName},
		},
		"spec": map[string]any{
			"podSelector": map[string]any{},
			"policyTypes": []any{"Ingress"},
			"ingress": []any{map[string]any{
				"from": []any{map[string]any{
					"namespaceSelector": map[string]any{
						"matchLabels": map[string]any{"nimbusdb.io/component": "gateway"},
					},
				}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": int64(5432)}},
			}},
		},
	}}
	return []*unstructured.Unstructured{deny, allowGateway}
}

// BuildCluster renders the CNPG Cluster for a branch. Sizing: guaranteed QoS
// at min CU in v1 (requests == limits; vertical autoscaling is Phase 4).
// Instances by role: production 2 (streaming replica + failover), else 1.
func BuildCluster(w postgres.BranchWork) *unstructured.Unstructured {
	instances := 1
	if w.Branch.Role == domain.BranchProduction {
		instances = 2
	}
	cpuMilli := int(w.Branch.Compute.MinCU * 1000)
	memMi := int(w.Branch.Compute.MinCU * 4096)
	res := map[string]any{
		"cpu":    fmt.Sprintf("%dm", cpuMilli),
		"memory": fmt.Sprintf("%dMi", memMi),
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": ClusterGVK.GroupVersion().String(),
		"kind":       ClusterGVK.Kind,
		"metadata": map[string]any{
			"name":      ClusterName(w.Branch.ID),
			"namespace": NamespaceName(w.ProjectID),
			"labels":    commonLabels(w),
		},
		"spec": map[string]any{
			"instances": int64(instances),
			"imageName": fmt.Sprintf("ghcr.io/cloudnative-pg/postgresql:%d", w.PGVersion),
			"storage":   map[string]any{"size": "5Gi"},
			"resources": map[string]any{"requests": res, "limits": res},
			"postgresql": map[string]any{
				"parameters": map[string]any{
					// pg_stat_statements from day one (query insights, Phase 7).
					"shared_preload_libraries": "pg_stat_statements",
				},
			},
			"enableSuperuserAccess": false,
			// WAL archiving (barman-cloud plugin) is wired in the backup
			// slice of Phase 2; the reconciler owns the object shape.
		},
	}}
}

// BuildPooler renders the branch's transaction-mode PgBouncer
// (DATABASE_ARCHITECTURE §5 — max_prepared_statements on for
// extended-protocol clients like node-postgres/Drizzle).
func BuildPooler(w postgres.BranchWork) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": PoolerGVK.GroupVersion().String(),
		"kind":       PoolerGVK.Kind,
		"metadata": map[string]any{
			"name":      PoolerName(w.Branch.ID),
			"namespace": NamespaceName(w.ProjectID),
			"labels":    commonLabels(w),
		},
		"spec": map[string]any{
			"cluster":   map[string]any{"name": ClusterName(w.Branch.ID)},
			"instances": int64(1),
			"type":      "rw",
			"pgbouncer": map[string]any{
				"poolMode": "transaction",
				"parameters": map[string]any{
					"max_prepared_statements": "512",
					"default_pool_size":       "20",
				},
			},
		},
	}}
}

// routeEntry mirrors services/pg-gateway/internal/routes.Route's JSON shape
// (separate modules; the ConfigMap is the contract between them).
type routeEntry struct {
	Backend  string `json:"backend"`
	State    string `json:"state"`
	MaxConns int    `json:"max_conns"`
}

// BuildRoutesJSON renders the gateway route table from routable endpoints.
func BuildRoutesJSON(eps []postgres.RoutableEndpoint) ([]byte, error) {
	table := map[string]routeEntry{}
	for _, e := range eps {
		table[e.EndpointID] = routeEntry{
			Backend: BackendFor(e.Kind, e.BranchID, e.ProjectID),
			State:   string(e.State),
		}
	}
	return json.MarshalIndent(map[string]any{"endpoints": table}, "", "  ")
}

func BuildRoutesConfigMap(routesJSON []byte) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      RoutesConfigMap,
			"namespace": GatewayNamespace,
			"labels":    map[string]any{labelManagedBy: managerName},
		},
		"data": map[string]any{"routes.json": string(routesJSON)},
	}}
}
