package reconciler

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

func TestClusterCarriesBackupSpec(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchProduction)
	src := &fakeSource{work: []postgres.BranchWork{w}}
	e, kc, cfg := newEngineWithBackup(t, src)

	if err := e.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cluster := get(t, kc, "Cluster", "prj-01test", "br-01test")

	dest, _, _ := unstructured.NestedString(cluster.Object, "spec", "backup", "barmanObjectStore", "destinationPath")
	if dest != "s3://ndb-syd1-wal/prj_01test/br_01test" {
		t.Fatalf("destinationPath = %q — WAL streams must be isolated per project/branch", dest)
	}
	endpoint, _, _ := unstructured.NestedString(cluster.Object, "spec", "backup", "barmanObjectStore", "endpointURL")
	if endpoint != cfg.EndpointURL {
		t.Fatalf("endpointURL = %q", endpoint)
	}
	retention, _, _ := unstructured.NestedString(cluster.Object, "spec", "backup", "retentionPolicy")
	if retention != "7d" {
		t.Fatalf("retentionPolicy = %q, want 7d (branch retention_days)", retention)
	}
	secretName, _, _ := unstructured.NestedString(cluster.Object,
		"spec", "backup", "barmanObjectStore", "s3Credentials", "accessKeyId", "name")
	if secretName != "ndb-backup-credentials" {
		t.Fatalf("credentials secret = %q", secretName)
	}

	// ScheduledBackup exists, targets the cluster, and is spread off midnight.
	sb := &unstructured.Unstructured{}
	sb.SetGroupVersionKind(ScheduledBackupGVK)
	if err := kc.Get(context.Background(), client.ObjectKey{Namespace: "prj-01test", Name: "br-01test-nightly"}, sb); err != nil {
		t.Fatal(err)
	}
	target, _, _ := unstructured.NestedString(sb.Object, "spec", "cluster", "name")
	if target != "br-01test" {
		t.Fatalf("scheduled backup targets %q", target)
	}
	schedule, _, _ := unstructured.NestedString(sb.Object, "spec", "schedule")
	if strings.HasPrefix(schedule, "0 0 0 ") {
		t.Fatalf("schedule %q not spread (thundering-herd risk)", schedule)
	}
}

func TestScheduledBackupSpreadIsDeterministic(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	a, _, _ := unstructured.NestedString(BuildScheduledBackup(w).Object, "spec", "schedule")
	b, _, _ := unstructured.NestedString(BuildScheduledBackup(w).Object, "spec", "schedule")
	if a != b {
		t.Fatalf("schedule must be deterministic per branch: %q vs %q (reconciler idempotency)", a, b)
	}
}

func TestNoBackupSectionWithoutConfig(t *testing.T) {
	w := testWork(domain.StateProvisioning, domain.BranchDevelopment)
	cluster := BuildCluster(w, nil)
	if _, found, _ := unstructured.NestedMap(cluster.Object, "spec", "backup"); found {
		t.Fatal("nil backup config must omit spec.backup")
	}
}

func TestRecoveryClusterShape(t *testing.T) {
	source := testWork(domain.StateReady, domain.BranchProduction)
	cfg := &BackupConfig{BucketBase: "s3://ndb-syd1-wal", CredentialsSecret: "creds"}
	at := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	rec := BuildRecoveryCluster(source, RecoveryTarget{Time: at}, cfg, "verify-br-01test", "ndb-verify")

	if rec.GetName() != "verify-br-01test" || rec.GetNamespace() != "ndb-verify" {
		t.Fatalf("identity = %s/%s", rec.GetNamespace(), rec.GetName())
	}
	src2, _, _ := unstructured.NestedString(rec.Object, "spec", "bootstrap", "recovery", "source")
	if src2 != "origin" {
		t.Fatalf("recovery source = %q", src2)
	}
	tt, _, _ := unstructured.NestedString(rec.Object, "spec", "bootstrap", "recovery", "recoveryTarget", "targetTime")
	if tt != "2026-07-17T12:00:00Z" {
		t.Fatalf("targetTime = %q", tt)
	}
	externals, _, _ := unstructured.NestedSlice(rec.Object, "spec", "externalClusters")
	if len(externals) != 1 {
		t.Fatalf("externalClusters = %d", len(externals))
	}
	dest, _, _ := unstructured.NestedString(externals[0].(map[string]any), "barmanObjectStore", "destinationPath")
	if dest != "s3://ndb-syd1-wal/prj_01test/br_01test" {
		t.Fatalf("origin destination = %q", dest)
	}
	// The clone must not archive into its source's WAL stream.
	if _, found, _ := unstructured.NestedMap(rec.Object, "spec", "backup"); found {
		t.Fatal("recovery cluster must not carry a backup section")
	}

	// Latest-point recovery omits recoveryTarget.
	latest := BuildRecoveryCluster(source, RecoveryTarget{}, cfg, "v2", "ndb-verify")
	if _, found, _ := unstructured.NestedMap(latest.Object, "spec", "bootstrap", "recovery", "recoveryTarget"); found {
		t.Fatal("zero target must mean latest (no recoveryTarget)")
	}
}
