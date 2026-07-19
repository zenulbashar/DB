package reconciler

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

func forkWork(parentID string, at *time.Time) postgres.BranchWork {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	w.Branch.ParentID = &parentID
	w.Branch.BootstrapAt = at
	return w
}

func TestBranchedClusterBootstrapsFromParentArchive(t *testing.T) {
	child := forkWork("br_01parent", nil)
	parent := postgres.BranchWork{ProjectID: "prj_01test", Branch: domain.Branch{ID: "br_01parent"}}
	cfg := &BackupConfig{BucketBase: "s3://ndb-syd1-wal", CredentialsSecret: "creds"}

	u := BuildBranchedCluster(child, parent, RecoveryTarget{}, cfg)
	spec := u.Object["spec"].(map[string]any)

	rec, _, _ := unstructured.NestedMap(spec, "bootstrap", "recovery")
	if rec["source"] != "origin" {
		t.Fatalf("bootstrap.recovery.source = %v, want origin", rec["source"])
	}
	if _, ok := rec["recoveryTarget"]; ok {
		t.Fatal("branch-from-now must not set a recoveryTarget")
	}
	// The origin external cluster points at the PARENT's archive.
	ext := spec["externalClusters"].([]any)[0].(map[string]any)
	dp, _, _ := unstructured.NestedString(ext, "barmanObjectStore", "destinationPath")
	if dp != "s3://ndb-syd1-wal/prj_01test/br_01parent" {
		t.Fatalf("origin destinationPath = %q, want the parent's archive", dp)
	}
	// The child keeps its OWN forward archive, at its OWN path (independent fork).
	childDP, _, _ := unstructured.NestedString(spec, "backup", "barmanObjectStore", "destinationPath")
	if childDP != "s3://ndb-syd1-wal/prj_01test/br_01test" {
		t.Fatalf("child archive destinationPath = %q, want the child's own path", childDP)
	}
	// Child's own compute (dev role → 1 instance), not the parent's.
	if inst, _, _ := unstructured.NestedInt64(spec, "instances"); inst != 1 {
		t.Fatalf("child instances = %d, want 1 (child's own spec)", inst)
	}
}

func TestBranchedClusterPITRTarget(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	child := forkWork("br_01parent", &at)
	parent := postgres.BranchWork{ProjectID: "prj_01test", Branch: domain.Branch{ID: "br_01parent"}}
	cfg := &BackupConfig{BucketBase: "s3://ndb-syd1-wal", CredentialsSecret: "creds"}

	u := BuildBranchedCluster(child, parent, RecoveryTarget{Time: at}, cfg)
	spec := u.Object["spec"].(map[string]any)
	target, _, _ := unstructured.NestedString(spec, "bootstrap", "recovery", "recoveryTarget", "targetTime")
	if target != "2026-01-02T03:04:05Z" {
		t.Fatalf("recoveryTarget.targetTime = %q, want the PITR target", target)
	}
}

func TestProvisionForkUsesRecoveryBootstrap(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{work: []postgres.BranchWork{forkWork("br_01parent", nil)}}
	e, kc, _ := newEngineWithBackup(t, src)
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	if _, ok, _ := unstructured.NestedMap(cluster.Object, "spec", "bootstrap", "recovery"); !ok {
		t.Fatal("a forked branch must provision via recovery bootstrap")
	}
	if _, ok, _ := unstructured.NestedSlice(cluster.Object, "spec", "externalClusters"); !ok {
		t.Fatal("a forked branch must reference the parent as an external cluster")
	}
}

func TestProvisionRootBranchUsesInitdb(t *testing.T) {
	ctx := context.Background()
	// A root branch (no parent) has no recovery bootstrap even with a backup cfg.
	src := &fakeSource{work: []postgres.BranchWork{testWork(domain.StateProvisioning, domain.BranchDevelopment)}}
	e, kc, _ := newEngineWithBackup(t, src)
	if err := e.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")
	if _, ok, _ := unstructured.NestedMap(cluster.Object, "spec", "bootstrap", "recovery"); ok {
		t.Fatal("the root branch must bootstrap empty (initdb), not recovery")
	}
}
