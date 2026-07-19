package reconciler

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

// A branch carrying a read (ro_pooled) endpoint provisions a 2-instance cluster
// (primary + hot-standby) and a dedicated type: ro pooler — even on a dev
// branch that would otherwise run a single instance.
func TestReadEndpointProvisionsReplicaAndROPooler(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	w.Endpoints = append(w.Endpoints, domain.Endpoint{
		ID: "ep_01ro", BranchID: "br_01test", Kind: domain.EndpointROPooled,
	})
	src := &fakeSource{work: []postgres.BranchWork{w}}
	e, kc := newEngine(t, src)
	if err := e.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	if inst, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances"); inst != 2 {
		t.Fatalf("read endpoint must scale the cluster to 2 instances, got %d", inst)
	}
	ro := get(t, kc, "Pooler", "prj-01test", "br-01test-ro-pooler")
	if typ, _, _ := unstructured.NestedString(ro.Object, "spec", "type"); typ != "ro" {
		t.Fatalf("ro pooler type = %s, want ro", typ)
	}
	// The rw pooler still exists.
	get(t, kc, "Pooler", "prj-01test", "br-01test-pooler")
}

// Adding a read endpoint to an already-ready branch is reconciled without a
// re-provision: the reconcile marks only the new endpoint ready, once the
// (now 2-instance) cluster is healthy.
func TestReconcileEndpointsMarksNewEndpointReady(t *testing.T) {
	ctx := context.Background()
	w := testWork(domain.StateReady, domain.BranchDevelopment)
	w.Endpoints = []domain.Endpoint{
		{ID: "ep_01dir", BranchID: "br_01test", Kind: domain.EndpointRWDirect, State: domain.StateReady},
		{ID: "ep_01pool", BranchID: "br_01test", Kind: domain.EndpointRWPooled, State: domain.StateReady},
		{ID: "ep_01ro", BranchID: "br_01test", Kind: domain.EndpointROPooled, State: domain.StateProvisioning},
	}
	src := &fakeSource{work: []postgres.BranchWork{w}}
	e, kc := newEngine(t, src)

	// First pass builds the ro pooler + scales the cluster; not ready yet.
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.endpointsReady) != 0 {
		t.Fatalf("endpoints marked ready before cluster healthy: %v", src.endpointsReady)
	}
	get(t, kc, "Pooler", "prj-01test", "br-01test-ro-pooler") // ro pooler exists

	// Simulate CNPG bringing the 2-instance cluster up.
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	_ = unstructured.SetNestedField(cluster.Object, int64(2), "status", "readyInstances")
	if err := kc.Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.endpointsReady) != 1 || src.endpointsReady[0] != "br_01test" {
		t.Fatalf("endpointsReady = %v, want [br_01test]", src.endpointsReady)
	}
	// The branch itself was never re-marked ready (it stayed ready throughout).
	if len(src.ready) != 0 {
		t.Fatalf("branch should not be re-provisioned: ready=%v", src.ready)
	}
}
