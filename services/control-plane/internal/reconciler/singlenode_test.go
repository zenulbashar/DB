package reconciler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
)

// Single-node capacity profile (ADR-022): no same-host HA, burstable tenant
// CPU, guaranteed memory. Off-profile behavior is covered by every other test
// in this package running with the default (singleNode=false).

func withSingleNode(t *testing.T) {
	t.Helper()
	SetSingleNode(true)
	t.Cleanup(func() { SetSingleNode(false) })
}

func TestSingleNodeProductionIsOneInstance(t *testing.T) {
	withSingleNode(t)
	w := testWork(domain.StateProvisioning, domain.BranchProduction)
	cluster := BuildCluster(w, nil)

	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	if instances != 1 {
		t.Fatalf("single-node production instances = %d, want 1 (same-host replica buys nothing)", instances)
	}
	if _, found, _ := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "synchronous"); found {
		t.Fatal("single-instance cluster must not render sync replication")
	}
}

// A read endpoint still forces a standby even on single-node — it is
// functionally required to serve reads, availability aside.
func TestSingleNodeReadEndpointStillGetsStandby(t *testing.T) {
	withSingleNode(t)
	w := testWork(domain.StateReady, domain.BranchDevelopment)
	w.Endpoints = append(w.Endpoints, domain.Endpoint{
		ID: "ep_01ro", BranchID: w.Branch.ID, Kind: domain.EndpointROPooled,
	})
	cluster := BuildCluster(w, nil)
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	if instances != 2 {
		t.Fatalf("read-endpoint branch instances = %d, want 2 even on single-node", instances)
	}
}

func TestSingleNodePoolersAreSingleReplica(t *testing.T) {
	withSingleNode(t)
	w := testWork(domain.StateReady, domain.BranchProduction)
	for _, p := range []*unstructured.Unstructured{BuildPooler(w), BuildROPooler(w)} {
		if n, _, _ := unstructured.NestedInt64(p.Object, "spec", "instances"); n != 1 {
			t.Fatalf("%s single-node pooler instances = %d, want 1", p.GetName(), n)
		}
	}
	// Scale-to-zero still wins.
	suspended := testWork(domain.StateSuspended, domain.BranchProduction)
	if n, _, _ := unstructured.NestedInt64(BuildPooler(suspended).Object, "spec", "instances"); n != 0 {
		t.Fatalf("suspended pooler instances = %d, want 0", n)
	}
}

// Burstable CPU: request = CU/4, limit = full CU. Memory guaranteed either way.
func TestSingleNodeTenantCPUIsBurstable(t *testing.T) {
	withSingleNode(t)
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment) // 0.25 CU min
	cluster := BuildCluster(w, nil)

	cpuReq, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "requests", "cpu")
	cpuLim, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "limits", "cpu")
	if cpuReq != "62m" || cpuLim != "250m" {
		t.Fatalf("0.25 CU single-node cpu = req %s / lim %s, want 62m / 250m", cpuReq, cpuLim)
	}
	memReq, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "requests", "memory")
	memLim, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "limits", "memory")
	if memReq != "1024Mi" || memLim != "1024Mi" {
		t.Fatalf("0.25 CU memory = req %s / lim %s, want guaranteed 1024Mi both", memReq, memLim)
	}
}

// Default profile keeps guaranteed QoS (request == limit) — regression guard
// for the resources split introduced by ADR-022.
func TestDefaultProfileKeepsGuaranteedCPU(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	cluster := BuildCluster(w, nil)
	cpuReq, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "requests", "cpu")
	cpuLim, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "limits", "cpu")
	if cpuReq != cpuLim || cpuReq != "250m" {
		t.Fatalf("default profile cpu = req %s / lim %s, want guaranteed 250m", cpuReq, cpuLim)
	}
}
