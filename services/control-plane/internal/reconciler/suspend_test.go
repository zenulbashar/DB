package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

// seedRunningCluster provisions a branch and simulates CNPG bringing it up
// (readyInstances = spec.instances), returning the shared fake client so a
// follow-up suspend/resume engine can act on the existing objects.
func seedRunningCluster(t *testing.T) client.Client {
	t.Helper()
	prov := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	e, kc := newEngine(t, &fakeSource{work: []postgres.BranchWork{prov}})
	if err := e.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	_ = unstructured.SetNestedField(cluster.Object, int64(1), "status", "readyInstances")
	if err := kc.Update(context.Background(), cluster); err != nil {
		t.Fatal(err)
	}
	return kc
}

func TestSuspendHibernatesAndMarksSuspended(t *testing.T) {
	ctx := context.Background()
	kc := seedRunningCluster(t)

	susp := testWork(domain.StateSuspending, domain.BranchDevelopment)
	src := &fakeSource{work: []postgres.BranchWork{susp}}
	e := New(src, kc, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// First pass applies hibernation + scales the pooler to zero, but the
	// cluster still reports 1 ready instance, so the branch must NOT be marked
	// suspended yet.
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	if cluster.GetAnnotations()[hibernationAnnotation] != "on" {
		t.Fatalf("hibernation annotation = %q, want on", cluster.GetAnnotations()[hibernationAnnotation])
	}
	pooler := get(t, kc, "Pooler", "prj-01test", "br-01test-pooler")
	if inst, _, _ := unstructured.NestedInt64(pooler.Object, "spec", "instances"); inst != 0 {
		t.Fatalf("pooler instances = %d, want 0 when suspended", inst)
	}
	// spec.instances stays at the role value — hibernation is via annotation.
	if inst, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances"); inst != 1 {
		t.Fatalf("cluster spec.instances = %d, want 1 (hibernation is annotation-driven)", inst)
	}
	if len(src.suspended) != 0 {
		t.Fatalf("branch marked suspended before CNPG hibernated: %v", src.suspended)
	}

	// Simulate CNPG completing hibernation (no ready instances).
	_ = unstructured.SetNestedField(cluster.Object, int64(0), "status", "readyInstances")
	if err := kc.Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.suspended) != 1 || src.suspended[0] != "br_01test" {
		t.Fatalf("suspended = %v, want [br_01test]", src.suspended)
	}
}

func TestResumeUnhibernatesAndMarksReady(t *testing.T) {
	ctx := context.Background()
	kc := seedRunningCluster(t)

	// Simulate the branch already hibernated: annotation on, no ready instances.
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	anns := cluster.GetAnnotations()
	if anns == nil {
		anns = map[string]string{}
	}
	anns[hibernationAnnotation] = "on"
	cluster.SetAnnotations(anns)
	_ = unstructured.SetNestedField(cluster.Object, int64(0), "status", "readyInstances")
	if err := kc.Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	resume := testWork(domain.StateResuming, domain.BranchDevelopment)
	src := &fakeSource{work: []postgres.BranchWork{resume}}
	e := New(src, kc, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// First pass clears hibernation + scales the pooler back up; cluster not
	// ready yet, so no ready mark.
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cluster = get(t, kc, "Cluster", "prj-01test", "br-01test")
	if cluster.GetAnnotations()[hibernationAnnotation] != "off" {
		t.Fatalf("hibernation annotation = %q, want off after resume", cluster.GetAnnotations()[hibernationAnnotation])
	}
	pooler := get(t, kc, "Pooler", "prj-01test", "br-01test-pooler")
	if inst, _, _ := unstructured.NestedInt64(pooler.Object, "spec", "instances"); inst != 1 {
		t.Fatalf("pooler instances = %d, want 1 after resume", inst)
	}
	if len(src.resumed) != 0 {
		t.Fatalf("branch marked resumed before cluster ready: %v", src.resumed)
	}

	// Simulate CNPG bringing the cluster back up.
	_ = unstructured.SetNestedField(cluster.Object, int64(1), "status", "readyInstances")
	if err := kc.Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.resumed) != 1 || src.resumed[0] != "br_01test" {
		t.Fatalf("resumed = %v, want [br_01test]", src.resumed)
	}
}

// A suspending branch whose cluster has not yet REPORTED instance status
// (status.readyInstances absent) must NOT be marked suspended — an absent field
// reads as 0 and would otherwise flip the branch before compute is observed
// down (audit finding: clusterHibernated must honor the NestedInt64 found flag).
func TestSuspendWaitsForReportedStatus(t *testing.T) {
	ctx := context.Background()
	// Provision the cluster but leave status.readyInstances unset (as a freshly
	// applied CNPG Cluster is before the operator reports status).
	seedEngine, kc := newEngine(t, &fakeSource{work: []postgres.BranchWork{testWork(domain.StateProvisioning, domain.BranchDevelopment)}})
	if err := seedEngine.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}

	susp := testWork(domain.StateSuspending, domain.BranchDevelopment)
	src := &fakeSource{work: []postgres.BranchWork{susp}}
	e := New(src, kc, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.suspended) != 0 {
		t.Fatalf("branch marked suspended while cluster status unreported: %v", src.suspended)
	}

	// Once CNPG actually reports zero ready instances, it is hibernated.
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	_ = unstructured.SetNestedField(cluster.Object, int64(0), "status", "readyInstances")
	if err := kc.Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.suspended) != 1 {
		t.Fatalf("suspended = %v, want [br_01test] once status reports 0", src.suspended)
	}
}

// The transitional states must present to the gateway as "suspended" so it
// holds/rejects (they are not connectable yet) while staying in the route table.
func TestRoutesMapTransitionalStatesToSuspended(t *testing.T) {
	src := &fakeSource{routable: []postgres.RoutableEndpoint{
		{EndpointID: "ep_ready", BranchID: "br_01test", ProjectID: "prj_01test",
			Kind: domain.EndpointRWDirect, State: domain.StateReady},
		{EndpointID: "ep_susp", BranchID: "br_01test", ProjectID: "prj_01test",
			Kind: domain.EndpointRWDirect, State: domain.StateSuspending},
		{EndpointID: "ep_res", BranchID: "br_01test", ProjectID: "prj_01test",
			Kind: domain.EndpointRWPooled, State: domain.StateResuming},
	}}
	e, kc := newEngine(t, src)
	if err := e.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cm := &unstructured.Unstructured{}
	cm.SetAPIVersion("v1")
	cm.SetKind("ConfigMap")
	if err := kc.Get(context.Background(), client.ObjectKey{Namespace: GatewayNamespace, Name: RoutesConfigMap}, cm); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := unstructured.NestedString(cm.Object, "data", "routes.json")
	var parsed struct {
		Endpoints map[string]struct {
			State string `json:"state"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Endpoints["ep_ready"].State != "ready" {
		t.Fatalf("ready endpoint state = %q", parsed.Endpoints["ep_ready"].State)
	}
	if parsed.Endpoints["ep_susp"].State != "suspended" {
		t.Fatalf("suspending endpoint state = %q, want suspended", parsed.Endpoints["ep_susp"].State)
	}
	if parsed.Endpoints["ep_res"].State != "suspended" {
		t.Fatalf("resuming endpoint state = %q, want suspended", parsed.Endpoints["ep_res"].State)
	}
}
