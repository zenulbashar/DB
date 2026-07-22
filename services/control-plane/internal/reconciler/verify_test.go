package reconciler

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

func getVerifyCluster(t *testing.T, kc client.Client, w postgres.BranchWork) (*unstructured.Unstructured, error) {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ClusterGVK)
	err := kc.Get(context.Background(),
		client.ObjectKey{Namespace: NamespaceName(w.ProjectID), Name: VerifyClusterName(w.Branch.ID)}, u)
	return u, err
}

// A due branch gets a recovery clone: bootstrapped from its own archive, no
// forward archiving (it must never write into the source's WAL stream), and
// the ledger marks it running.
func TestVerifyStartsRecoveryClone(t *testing.T) {
	w := testWork(domain.StateReady, domain.BranchProduction)
	src := &fakeSource{verifyDue: []postgres.BranchWork{w}}
	e, kc, _ := newEngineWithBackup(t, src)

	if err := e.VerifyRestores(context.Background(), 24*time.Hour, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(src.verifyStarted) != 1 || src.verifyStarted[0] != w.Branch.ID {
		t.Fatalf("verifyStarted = %v", src.verifyStarted)
	}
	clone, err := getVerifyCluster(t, kc, w)
	if err != nil {
		t.Fatalf("verify cluster not created: %v", err)
	}
	if _, found, _ := unstructured.NestedMap(clone.Object, "spec", "bootstrap", "recovery"); !found {
		t.Fatal("verify cluster must bootstrap by recovery")
	}
	if _, found, _ := unstructured.NestedMap(clone.Object, "spec", "backup"); found {
		t.Fatal("verify cluster must not archive (would corrupt the source stream)")
	}
}

// A healthy clone settles as PASS and is torn down.
func TestVerifySettlesPassAndTearsDown(t *testing.T) {
	w := testWork(domain.StateReady, domain.BranchProduction)
	src := &fakeSource{
		verifyRunning: []postgres.RestoreVerification{{
			BranchID: w.Branch.ID, ProjectID: w.ProjectID, Status: "running",
			StartedAt: time.Now().Add(-time.Minute),
		}},
	}
	e, kc, cfg := newEngineWithBackup(t, src)

	// Simulate CNPG having restored the clone to healthy.
	clone := BuildRecoveryCluster(w, RecoveryTarget{}, cfg, VerifyClusterName(w.Branch.ID), NamespaceName(w.ProjectID))
	_ = unstructured.SetNestedField(clone.Object, int64(2), "status", "readyInstances")
	if err := kc.Create(context.Background(), clone); err != nil {
		t.Fatal(err)
	}

	if err := e.VerifyRestores(context.Background(), 24*time.Hour, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if src.verifyOutcomes[w.Branch.ID] != "pass" {
		t.Fatalf("outcome = %q, want pass", src.verifyOutcomes[w.Branch.ID])
	}
	if _, err := getVerifyCluster(t, kc, w); !apierrors.IsNotFound(err) {
		t.Fatalf("verify cluster should be deleted after pass, got %v", err)
	}
}

// A clone that never becomes healthy inside the timeout settles as FAIL (the
// R-2 paging signal) and is torn down.
func TestVerifySettlesTimeoutAsFail(t *testing.T) {
	w := testWork(domain.StateReady, domain.BranchProduction)
	src := &fakeSource{
		verifyRunning: []postgres.RestoreVerification{{
			BranchID: w.Branch.ID, ProjectID: w.ProjectID, Status: "running",
			StartedAt: time.Now().Add(-time.Hour), // long past the timeout
		}},
	}
	e, kc, cfg := newEngineWithBackup(t, src)
	clone := BuildRecoveryCluster(w, RecoveryTarget{}, cfg, VerifyClusterName(w.Branch.ID), NamespaceName(w.ProjectID))
	if err := kc.Create(context.Background(), clone); err != nil { // never healthy
		t.Fatal(err)
	}

	if err := e.VerifyRestores(context.Background(), 24*time.Hour, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	got := src.verifyOutcomes[w.Branch.ID]
	if !strings.HasPrefix(got, "fail:") || !strings.Contains(got, "R-2") {
		t.Fatalf("outcome = %q, want a fail citing R-2", got)
	}
	if _, err := getVerifyCluster(t, kc, w); !apierrors.IsNotFound(err) {
		t.Fatalf("verify cluster should be deleted after fail, got %v", err)
	}
}

// A running row with no clone (crash between record and create) is settled as
// a timeout fail, not skipped silently — and a young one is left to settle.
func TestVerifyCrashWindowSettlesByTimeout(t *testing.T) {
	w := testWork(domain.StateReady, domain.BranchProduction)
	young := postgres.RestoreVerification{
		BranchID: w.Branch.ID, ProjectID: w.ProjectID, Status: "running",
		StartedAt: time.Now().Add(-time.Minute),
	}
	src := &fakeSource{verifyRunning: []postgres.RestoreVerification{young}}
	e, _, _ := newEngineWithBackup(t, src)

	if err := e.VerifyRestores(context.Background(), 24*time.Hour, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(src.verifyOutcomes) != 0 {
		t.Fatalf("young crash-window row must not settle yet: %v", src.verifyOutcomes)
	}

	src.verifyRunning[0].StartedAt = time.Now().Add(-time.Hour)
	if err := e.VerifyRestores(context.Background(), 24*time.Hour, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(src.verifyOutcomes[w.Branch.ID], "fail:") {
		t.Fatalf("stale crash-window row must fail by timeout: %v", src.verifyOutcomes)
	}
}

// Without a backup config there is nothing to verify — the loop is a no-op.
func TestVerifyNoopWithoutBackupConfig(t *testing.T) {
	w := testWork(domain.StateReady, domain.BranchProduction)
	src := &fakeSource{verifyDue: []postgres.BranchWork{w}}
	e, _ := newEngine(t, src) // no backup config
	if err := e.VerifyRestores(context.Background(), 24*time.Hour, time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(src.verifyStarted) != 0 {
		t.Fatalf("verify must be a no-op without an archive, started %v", src.verifyStarted)
	}
}
