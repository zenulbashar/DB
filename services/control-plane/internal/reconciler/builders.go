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

	// hibernationAnnotation is CNPG's declarative hibernation switch. We toggle
	// it (rather than mutating spec.instances) because instances=0 is rejected
	// by the CNPG webhook and because the annotation is the operator-blessed way
	// to cleanly shut Postgres down while retaining the PVCs (ADR-003, ADR-014).
	hibernationAnnotation = "cnpg.io/hibernation"
)

// suspendedCompute reports whether a branch state means its compute should be
// hibernated / scaled to zero (suspending is mid-flight to suspended; both
// hold the compute down).
func suspendedCompute(state domain.ResourceState) bool {
	return state == domain.StateSuspending || state == domain.StateSuspended
}

// K8s object names: ULID-based IDs are already lowercase; swap the type
// separator for a DNS-safe dash. prj_01x → prj-01x, br_01y → br-01y.
func NamespaceName(projectID string) string { return strings.ReplaceAll(projectID, "_", "-") }
func ClusterName(branchID string) string    { return strings.ReplaceAll(branchID, "_", "-") }
func PoolerName(branchID string) string     { return ClusterName(branchID) + "-pooler" }
func ROPoolerName(branchID string) string   { return ClusterName(branchID) + "-ro-pooler" }

// BackendFor derives the in-cluster service address the gateway dials for an
// endpoint (DATABASE_ARCHITECTURE §5 wiring). The read endpoint routes through
// its own PgBouncer (type: ro), which fans reads out to the cluster's replicas.
func BackendFor(kind domain.EndpointKind, branchID, projectID string) string {
	ns := NamespaceName(projectID)
	switch kind {
	case domain.EndpointRWPooled:
		return fmt.Sprintf("%s.%s.svc:5432", PoolerName(branchID), ns)
	case domain.EndpointROPooled:
		return fmt.Sprintf("%s.%s.svc:5432", ROPoolerName(branchID), ns)
	default: // rw_direct
		return fmt.Sprintf("%s-rw.%s.svc:5432", ClusterName(branchID), ns)
	}
}

// hasReadEndpoint reports whether the branch exposes a read (ro_pooled)
// endpoint — which requires a replica to read from, so the cluster is scaled to
// at least two instances (ADR: read replicas).
func hasReadEndpoint(w postgres.BranchWork) bool {
	for _, e := range w.Endpoints {
		if e.Kind == domain.EndpointROPooled && e.State != domain.StateDeleting {
			return true
		}
	}
	return false
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

// Namespaces the tenant network policies must reach. Overridable via the
// reconciler config later; constants keep the builder pure and testable.
const (
	OperatorNamespace   = "cnpg-system"
	MonitoringNamespace = "nimbusdb-monitoring"
	pgPort              = int64(5432)
	instanceMgrPort     = int64(8000) // CNPG instance-manager REST
	exporterPort        = int64(9187) // postgres_exporter metrics
)

func np(name, ns string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata": map[string]any{
			"name": name, "namespace": ns,
			"labels": map[string]any{labelManagedBy: managerName},
		},
		"spec": spec,
	}}
}

func nsSelector(name string) map[string]any {
	return map[string]any{"namespaceSelector": map[string]any{
		"matchLabels": map[string]any{"kubernetes.io/metadata.name": name},
	}}
}

func tcp(ports ...int64) []any {
	out := make([]any, len(ports))
	for i, p := range ports {
		out[i] = map[string]any{"protocol": "TCP", "port": p}
	}
	return out
}

// BuildNetworkPolicies isolates a project namespace while leaving the paths
// CNPG needs to run a healthy HA cluster open (MULTI_TENANCY §2). The original
// ingress-only default-deny + gateway allow was too tight (audit finding): it
// blocked intra-cluster streaming replication, the CNPG operator, and metrics
// scraping, and left egress wide open. This emits:
//   - default-deny for BOTH ingress and egress (podSelector {})
//   - ingress allows: gateway (5432), same-namespace replication (5432),
//     CNPG operator (5432 + instance-manager 8000), monitoring (exporter 9187)
//   - egress allows: DNS, same-namespace replication, CNPG operator, and 443
//     for WAL archive to object storage — everything else denied.
func BuildNetworkPolicies(w postgres.BranchWork) []*unstructured.Unstructured {
	ns := NamespaceName(w.ProjectID)
	sameNS := map[string]any{"podSelector": map[string]any{}}

	deny := np("default-deny", ns, map[string]any{
		"podSelector": map[string]any{},
		"policyTypes": []any{"Ingress", "Egress"},
	})

	allowIngress := np("allow-postgres-ingress", ns, map[string]any{
		"podSelector": map[string]any{},
		"policyTypes": []any{"Ingress"},
		"ingress": []any{
			// Client traffic from the gateway.
			map[string]any{
				"from":  []any{nsSelector(GatewayNamespaceLabel())},
				"ports": tcp(pgPort),
			},
			// Streaming replication between this cluster's own pods.
			map[string]any{
				"from":  []any{sameNS},
				"ports": tcp(pgPort),
			},
			// CNPG operator: SQL + instance-manager REST.
			map[string]any{
				"from":  []any{nsSelector(OperatorNamespace)},
				"ports": tcp(pgPort, instanceMgrPort),
			},
			// Metrics scraping.
			map[string]any{
				"from":  []any{nsSelector(MonitoringNamespace)},
				"ports": tcp(exporterPort),
			},
		},
	})

	allowEgress := np("allow-postgres-egress", ns, map[string]any{
		"podSelector": map[string]any{},
		"policyTypes": []any{"Egress"},
		"egress": []any{
			// DNS.
			map[string]any{
				"to": []any{nsSelector("kube-system")},
				"ports": []any{
					map[string]any{"protocol": "UDP", "port": int64(53)},
					map[string]any{"protocol": "TCP", "port": int64(53)},
				},
			},
			// Replication to same-namespace pods.
			map[string]any{"to": []any{sameNS}, "ports": tcp(pgPort)},
			// CNPG operator instance-manager.
			map[string]any{"to": []any{nsSelector(OperatorNamespace)}, "ports": tcp(instanceMgrPort)},
			// WAL archive / base backups to S3-compatible object storage.
			map[string]any{"ports": tcp(443)},
		},
	})

	return []*unstructured.Unstructured{deny, allowIngress, allowEgress}
}

// GatewayNamespaceLabel is the metadata.name label of the gateway namespace,
// matched by the ingress allow.
func GatewayNamespaceLabel() string { return GatewayNamespace }

// BuildCluster renders the CNPG Cluster for a branch. Sizing: guaranteed QoS
// at min CU in v1 (requests == limits; vertical autoscaling is Phase 4).
// Instances by role: production 2 (streaming replica + failover), else 1.
// A non-nil backup config attaches continuous WAL archiving + retention.
func BuildCluster(w postgres.BranchWork, backup *BackupConfig) *unstructured.Unstructured {
	instances := 1
	if w.Branch.Role == domain.BranchProduction {
		instances = 2
	}
	// A read endpoint needs at least one hot-standby replica to serve reads.
	if hasReadEndpoint(w) && instances < 2 {
		instances = 2
	}
	cpuMilli := int(w.Branch.Compute.MinCU * 1000)
	memMi := int(w.Branch.Compute.MinCU * 4096)
	res := map[string]any{
		"cpu":    fmt.Sprintf("%dm", cpuMilli),
		"memory": fmt.Sprintf("%dMi", memMi),
	}
	spec := map[string]any{
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
	}
	if backup != nil {
		spec["backup"] = backup.barmanSection(w)
	}
	// Hibernation is always emitted explicitly (on when suspended, off
	// otherwise) so a resume deterministically clears a prior "on" via ensure()'s
	// annotation merge — an absent key would leave the cluster hibernated.
	hibernation := "off"
	if suspendedCompute(w.Branch.State) {
		hibernation = "on"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": ClusterGVK.GroupVersion().String(),
		"kind":       ClusterGVK.Kind,
		"metadata": map[string]any{
			"name":        ClusterName(w.Branch.ID),
			"namespace":   NamespaceName(w.ProjectID),
			"labels":      commonLabels(w),
			"annotations": map[string]any{hibernationAnnotation: hibernation},
		},
		"spec": spec,
	}}
}

// BuildPooler renders the branch's transaction-mode PgBouncer
// (DATABASE_ARCHITECTURE §5 — max_prepared_statements on for
// extended-protocol clients like node-postgres/Drizzle).
func BuildPooler(w postgres.BranchWork) *unstructured.Unstructured {
	// Scale the pooler to zero alongside the hibernated cluster (DATABASE_
	// ARCHITECTURE §7); a running pooler in front of a hibernated cluster would
	// just refuse connects and burn a pod.
	instances := int64(1)
	if suspendedCompute(w.Branch.State) {
		instances = 0
	}
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
			"instances": instances,
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

// BuildROPooler renders the branch's READ pooler: a transaction-mode PgBouncer
// of type `ro` that fans reads out to the cluster's hot-standby replicas (the
// ro_pooled endpoint; read replicas). Scaled to zero alongside a hibernated
// cluster, like the rw pooler.
func BuildROPooler(w postgres.BranchWork) *unstructured.Unstructured {
	instances := int64(1)
	if suspendedCompute(w.Branch.State) {
		instances = 0
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": PoolerGVK.GroupVersion().String(),
		"kind":       PoolerGVK.Kind,
		"metadata": map[string]any{
			"name":      ROPoolerName(w.Branch.ID),
			"namespace": NamespaceName(w.ProjectID),
			"labels":    commonLabels(w),
		},
		"spec": map[string]any{
			"cluster":   map[string]any{"name": ClusterName(w.Branch.ID)},
			"instances": instances,
			"type":      "ro",
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
// (separate modules; the ConfigMap is the contract between them). BranchID lets
// the gateway map a connecting suspended endpoint to the branch it must wake
// (ADR-014).
type routeEntry struct {
	Backend  string `json:"backend"`
	State    string `json:"state"`
	MaxConns int    `json:"max_conns"`
	BranchID string `json:"branch_id"`
}

// BuildRoutesJSON renders the gateway route table from routable endpoints.
func BuildRoutesJSON(eps []postgres.RoutableEndpoint) ([]byte, error) {
	table := map[string]routeEntry{}
	for _, e := range eps {
		table[e.EndpointID] = routeEntry{
			Backend:  BackendFor(e.Kind, e.BranchID, e.ProjectID),
			State:    routeState(e.State),
			MaxConns: e.MaxConns,
			BranchID: e.BranchID,
		}
	}
	return json.MarshalIndent(map[string]any{"endpoints": table}, "", "  ")
}

// routeState maps a control-plane resource state to the two-valued route state
// the gateway understands (routes.State is ready|suspended). The transitional
// suspending/resuming states present as "suspended": until the branch is fully
// ready its backend is not connectable, so the gateway must hold/reject exactly
// as for a settled suspended endpoint. Keeping the gateway's contract two-valued
// means this increment needs no gateway change — hold-and-wake replaces the
// "suspended" rejection in the next increment (ADR-014).
func routeState(s domain.ResourceState) string {
	if s == domain.StateReady {
		return string(domain.StateReady)
	}
	return string(domain.StateSuspended)
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
