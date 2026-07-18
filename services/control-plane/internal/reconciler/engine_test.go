package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

// fakeSource implements Source in memory.
type fakeSource struct {
	work      []postgres.BranchWork
	routable  []postgres.RoutableEndpoint
	ready     []string
	suspended []string
	resumed   []string
	tornDown  []string
	liveCount map[string]int
}

func (f *fakeSource) ListReconcileWork(context.Context) ([]postgres.BranchWork, error) {
	return f.work, nil
}
func (f *fakeSource) MarkBranchReady(_ context.Context, id string) error {
	f.ready = append(f.ready, id)
	return nil
}
func (f *fakeSource) MarkBranchSuspended(_ context.Context, id string) error {
	f.suspended = append(f.suspended, id)
	return nil
}
func (f *fakeSource) MarkBranchResumed(_ context.Context, id string) error {
	f.resumed = append(f.resumed, id)
	return nil
}
func (f *fakeSource) FinishBranchTeardown(_ context.Context, id string) error {
	f.tornDown = append(f.tornDown, id)
	return nil
}
func (f *fakeSource) CountLiveBranches(_ context.Context, projectID string) (int, error) {
	return f.liveCount[projectID], nil
}
func (f *fakeSource) ListRoutableEndpoints(context.Context) ([]postgres.RoutableEndpoint, error) {
	return f.routable, nil
}

func testWork(state domain.ResourceState, role domain.BranchRole) postgres.BranchWork {
	return postgres.BranchWork{
		Branch: domain.Branch{
			ID: "br_01test", ProjectID: "prj_01test", OrgID: "org_01test",
			Name: "main", Role: role, State: state, RetentionDays: 7,
			Compute: domain.Compute{MinCU: 0.25, MaxCU: 2, SuspendTimeoutS: 300},
		},
		ProjectID: "prj_01test", OrgID: "org_01test", Region: "syd1", PGVersion: 17,
		Endpoints: []domain.Endpoint{
			{ID: "ep_01dir", BranchID: "br_01test", Kind: domain.EndpointRWDirect},
			{ID: "ep_01pool", BranchID: "br_01test", Kind: domain.EndpointRWPooled},
		},
	}
}

func newEngine(t *testing.T, src Source, objs ...client.Object) (*Engine, client.Client) {
	t.Helper()
	kc := fake.NewClientBuilder().WithScheme(Scheme()).WithObjects(objs...).Build()
	return New(src, kc, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), kc
}

func newEngineWithBackup(t *testing.T, src Source) (*Engine, client.Client, *BackupConfig) {
	t.Helper()
	cfg := &BackupConfig{
		BucketBase:        "s3://ndb-syd1-wal",
		EndpointURL:       "https://minio.platform.svc:9000",
		CredentialsSecret: "ndb-backup-credentials",
	}
	kc := fake.NewClientBuilder().WithScheme(Scheme()).Build()
	return New(src, kc, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))), kc, cfg
}

func get(t *testing.T, kc client.Client, gvk string, ns, name string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	switch gvk {
	case "Cluster":
		u.SetGroupVersionKind(ClusterGVK)
	case "Pooler":
		u.SetGroupVersionKind(PoolerGVK)
	default:
		t.Fatalf("unknown gvk %s", gvk)
	}
	if err := kc.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, u); err != nil {
		t.Fatalf("get %s %s/%s: %v", gvk, ns, name, err)
	}
	return u
}

func TestProvisionCreatesResources(t *testing.T) {
	src := &fakeSource{work: []postgres.BranchWork{testWork(domain.StateProvisioning, domain.BranchProduction)}}
	e, kc := newEngine(t, src)
	ctx := context.Background()

	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}

	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	if instances != 2 {
		t.Fatalf("production branch instances = %d, want 2", instances)
	}
	image, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName")
	if image != "ghcr.io/cloudnative-pg/postgresql:17" {
		t.Fatalf("image = %s", image)
	}
	cpu, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "requests", "cpu")
	if cpu != "250m" {
		t.Fatalf("cpu request = %s, want 250m (0.25 CU)", cpu)
	}
	superuser, _, _ := unstructured.NestedBool(cluster.Object, "spec", "enableSuperuserAccess")
	if superuser {
		t.Fatal("tenant clusters must not enable superuser access")
	}

	pooler := get(t, kc, "Pooler", "prj-01test", "br-01test-pooler")
	mode, _, _ := unstructured.NestedString(pooler.Object, "spec", "pgbouncer", "poolMode")
	if mode != "transaction" {
		t.Fatalf("pool mode = %s", mode)
	}
	prepared, _, _ := unstructured.NestedString(pooler.Object, "spec", "pgbouncer", "parameters", "max_prepared_statements")
	if prepared != "512" {
		t.Fatal("max_prepared_statements must be enabled for extended-protocol clients")
	}

	// Namespace protections exist: default-deny both directions, plus the
	// ingress/egress allow-lists that keep CNPG functional.
	npNames := []string{"default-deny", "allow-postgres-ingress", "allow-postgres-egress"}
	for _, name := range npNames {
		np := &unstructured.Unstructured{}
		np.SetAPIVersion("networking.k8s.io/v1")
		np.SetKind("NetworkPolicy")
		if err := kc.Get(ctx, client.ObjectKey{Namespace: "prj-01test", Name: name}, np); err != nil {
			t.Fatalf("network policy %s: %v", name, err)
		}
	}
	// The default-deny must cover BOTH ingress and egress (egress was missing
	// before the audit).
	deny := &unstructured.Unstructured{}
	deny.SetAPIVersion("networking.k8s.io/v1")
	deny.SetKind("NetworkPolicy")
	if err := kc.Get(ctx, client.ObjectKey{Namespace: "prj-01test", Name: "default-deny"}, deny); err != nil {
		t.Fatal(err)
	}
	types, _, _ := unstructured.NestedStringSlice(deny.Object, "spec", "policyTypes")
	hasIngress, hasEgress := false, false
	for _, tp := range types {
		if tp == "Ingress" {
			hasIngress = true
		}
		if tp == "Egress" {
			hasEgress = true
		}
	}
	if !hasIngress || !hasEgress {
		t.Fatalf("default-deny must cover Ingress AND Egress, got %v", types)
	}

	// Cluster not ready yet → branch must not be marked ready.
	if len(src.ready) != 0 {
		t.Fatalf("branch marked ready before cluster status: %v", src.ready)
	}
}

func TestProvisionMarksReadyWhenClusterHealthy(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	src := &fakeSource{work: []postgres.BranchWork{w}}
	e, kc := newEngine(t, src)
	ctx := context.Background()

	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate CNPG bringing the (single-instance dev) cluster up.
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	_ = unstructured.SetNestedField(cluster.Object, int64(1), "status", "readyInstances")
	if err := kc.Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.ready) != 1 || src.ready[0] != "br_01test" {
		t.Fatalf("ready = %v, want [br_01test]", src.ready)
	}
	// Idempotency: a third pass creates nothing new and errors nothing.
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTeardownRemovesResourcesAndNamespaceWhenLast(t *testing.T) {
	w := testWork(domain.StateDeleting, domain.BranchDevelopment)
	src := &fakeSource{work: []postgres.BranchWork{w}, liveCount: map[string]int{"prj_01test": 0}}

	// Pre-seed the objects a previous provision created.
	prov := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	seedEngine, kc := newEngine(t, &fakeSource{work: []postgres.BranchWork{prov}})
	if err := seedEngine.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := New(src, kc, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := e.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ClusterGVK)
	err := kc.Get(context.Background(), client.ObjectKey{Namespace: "prj-01test", Name: "br-01test"}, u)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("cluster should be gone, got err=%v", err)
	}
	if len(src.tornDown) != 1 || src.tornDown[0] != "br_01test" {
		t.Fatalf("tornDown = %v", src.tornDown)
	}
}

func TestRoutesConfigMapPublished(t *testing.T) {
	src := &fakeSource{routable: []postgres.RoutableEndpoint{
		{EndpointID: "ep_01dir", BranchID: "br_01test", ProjectID: "prj_01test",
			Kind: domain.EndpointRWDirect, State: domain.StateReady},
		{EndpointID: "ep_01pool", BranchID: "br_01test", ProjectID: "prj_01test",
			Kind: domain.EndpointRWPooled, State: domain.StateSuspended},
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
			Backend  string `json:"backend"`
			State    string `json:"state"`
			BranchID string `json:"branch_id"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	direct := parsed.Endpoints["ep_01dir"]
	if direct.Backend != "br-01test-rw.prj-01test.svc:5432" || direct.State != "ready" {
		t.Fatalf("direct route = %+v", direct)
	}
	// branch_id must be emitted so the gateway can wake the branch (ADR-014).
	if direct.BranchID != "br_01test" {
		t.Fatalf("route branch_id = %q, want br_01test", direct.BranchID)
	}
	pooled := parsed.Endpoints["ep_01pool"]
	if pooled.Backend != "br-01test-pooler.prj-01test.svc:5432" || pooled.State != "suspended" {
		t.Fatalf("pooled route = %+v", pooled)
	}
}

func TestBackendFor(t *testing.T) {
	cases := map[domain.EndpointKind]string{
		domain.EndpointRWDirect: "br-01x-rw.prj-01y.svc:5432",
		domain.EndpointRWPooled: "br-01x-pooler.prj-01y.svc:5432",
		domain.EndpointROPooled: "br-01x-ro.prj-01y.svc:5432",
	}
	for kind, want := range cases {
		if got := BackendFor(kind, "br_01x", "prj_01y"); got != want {
			t.Errorf("BackendFor(%s) = %s, want %s", kind, got, want)
		}
	}
}
