package reconciler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
)

// ADR-019: HA-tier guarantees are builder-enforced — synchronous replication
// with preferred durability + controlled switchover on the cluster, and two
// pooler replicas.

func TestHAClusterGetsSyncReplicationAndSwitchover(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchProduction)
	cluster := BuildCluster(w, nil)

	method, found, _ := unstructured.NestedString(cluster.Object, "spec", "postgresql", "synchronous", "method")
	if !found || method != "any" {
		t.Fatalf("HA cluster synchronous.method = %q (found=%v), want any", method, found)
	}
	number, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "postgresql", "synchronous", "number")
	if number != 1 {
		t.Fatalf("synchronous.number = %d, want 1", number)
	}
	durability, _, _ := unstructured.NestedString(cluster.Object, "spec", "postgresql", "synchronous", "dataDurability")
	if durability != "preferred" {
		t.Fatalf("synchronous.dataDurability = %q, want preferred (availability-first, ADR-019)", durability)
	}
	strategy, _, _ := unstructured.NestedString(cluster.Object, "spec", "primaryUpdateStrategy")
	if strategy != "unsupervised" {
		t.Fatalf("primaryUpdateStrategy = %q, want unsupervised", strategy)
	}
	method2, _, _ := unstructured.NestedString(cluster.Object, "spec", "primaryUpdateMethod")
	if method2 != "switchover" {
		t.Fatalf("primaryUpdateMethod = %q, want switchover", method2)
	}
}

func TestDevClusterStaysAsyncSingleInstance(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	cluster := BuildCluster(w, nil)

	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	if instances != 1 {
		t.Fatalf("dev branch instances = %d, want 1", instances)
	}
	if _, found, _ := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "synchronous"); found {
		t.Fatal("dev cluster must not render a synchronous block")
	}
	if _, found, _ := unstructured.NestedString(cluster.Object, "spec", "primaryUpdateStrategy"); found {
		t.Fatal("dev cluster must not render primaryUpdateStrategy")
	}
}

// A read endpoint makes any branch HA tier — the same trigger that forces a
// standby also gets the sync-replication guarantees.
func TestReadEndpointBranchIsHATier(t *testing.T) {
	w := testWork(domain.StateReady, domain.BranchDevelopment)
	w.Endpoints = append(w.Endpoints, domain.Endpoint{
		ID: "ep_01ro", BranchID: w.Branch.ID, Kind: domain.EndpointROPooled,
	})
	cluster := BuildCluster(w, nil)
	if _, found, _ := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "synchronous"); !found {
		t.Fatal("read-endpoint branch must get synchronous replication (HA tier)")
	}
}

// Branched clusters (data forks) derive from BuildCluster and must inherit the
// HA settings of their own tier.
func TestBranchedClusterInheritsHASettings(t *testing.T) {
	child := testWork(domain.StateProvisioning, domain.BranchProduction)
	parent := testWork(domain.StateReady, domain.BranchProduction)
	parent.Branch.ID = "br_01parent"
	cfg := &BackupConfig{BucketBase: "s3://b", CredentialsSecret: "s"}

	cluster := BuildBranchedCluster(child, parent, RecoveryTarget{}, cfg)
	if _, found, _ := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "synchronous"); !found {
		t.Fatal("branched HA cluster must inherit synchronous replication")
	}
}

func TestPoolerInstancesByTier(t *testing.T) {
	// HA tier → 2 replicas on both poolers.
	ha := testWork(domain.StateReady, domain.BranchProduction)
	for _, p := range []*unstructured.Unstructured{BuildPooler(ha), BuildROPooler(ha)} {
		if n, _, _ := unstructured.NestedInt64(p.Object, "spec", "instances"); n != 2 {
			t.Fatalf("%s HA pooler instances = %d, want 2", p.GetName(), n)
		}
	}

	// Dev tier → 1 replica.
	dev := testWork(domain.StateReady, domain.BranchDevelopment)
	if n, _, _ := unstructured.NestedInt64(BuildPooler(dev).Object, "spec", "instances"); n != 1 {
		t.Fatalf("dev pooler instances = %d, want 1", n)
	}

	// Suspended → 0 replicas, even on HA tier (scale-to-zero wins).
	suspended := testWork(domain.StateSuspended, domain.BranchProduction)
	for _, p := range []*unstructured.Unstructured{BuildPooler(suspended), BuildROPooler(suspended)} {
		if n, _, _ := unstructured.NestedInt64(p.Object, "spec", "instances"); n != 0 {
			t.Fatalf("%s suspended pooler instances = %d, want 0", p.GetName(), n)
		}
	}
}
