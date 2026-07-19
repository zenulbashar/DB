package reconciler

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

// BuildCluster sizes CPU/memory from the branch's current CU (autoscaled),
// falling back to min when unset.
func TestClusterSizedFromCurrentCU(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	w.Branch.Compute.CurrentCU = 2 // 2 CU
	u := BuildCluster(w, nil)
	cpu, _, _ := unstructured.NestedString(u.Object, "spec", "resources", "requests", "cpu")
	if cpu != "2000m" {
		t.Fatalf("cpu request = %s, want 2000m (2 CU current)", cpu)
	}

	// Unset current_cu → falls back to min_cu (0.25).
	w.Branch.Compute.CurrentCU = 0
	u = BuildCluster(w, nil)
	cpu, _, _ = unstructured.NestedString(u.Object, "spec", "resources", "requests", "cpu")
	if cpu != "250m" {
		t.Fatalf("cpu request = %s, want 250m (min fallback)", cpu)
	}
}

// A resizing branch re-applies the cluster at the new size and flips back to
// ready once healthy; endpoints/routing are untouched.
func TestResizeReappliesClusterAndMarksReady(t *testing.T) {
	ctx := context.Background()
	w := testWork(domain.StateResizing, domain.BranchDevelopment)
	w.Branch.Compute.CurrentCU = 4
	src := &fakeSource{work: []postgres.BranchWork{w}}
	e, kc := newEngine(t, src)

	// First pass applies the resize; cluster not reported ready yet → not marked.
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	if cpu, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "requests", "cpu"); cpu != "4000m" {
		t.Fatalf("resized cpu = %s, want 4000m", cpu)
	}
	if len(src.resized) != 0 {
		t.Fatalf("marked resized before cluster healthy: %v", src.resized)
	}

	// CNPG reports the (single dev instance) healthy at the new size.
	_ = unstructured.SetNestedField(cluster.Object, int64(1), "status", "readyInstances")
	if err := kc.Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.resized) != 1 || src.resized[0] != "br_01test" {
		t.Fatalf("resized = %v, want [br_01test]", src.resized)
	}
}
