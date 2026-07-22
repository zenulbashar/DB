package reconciler

// Restore verification (R-2): a backup you haven't restored is a hope, not a
// backup. The verify loop periodically restores each ready branch's WAL
// archive into an ephemeral recovery cluster beside the branch (same
// namespace — that's where the archive credentials secret lives), records
// pass/fail, and tears the clone down. Asynchronous across ticks in the same
// crash-safe style as ReconcileOnce: state lives in the store, every step is
// idempotent.

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

// VerifyClusterName is the ephemeral recovery clone's name for a branch.
func VerifyClusterName(branchID string) string { return ClusterName(branchID) + "-verify" }

// VerifyRestores runs one pass of the verification loop: settle in-flight
// verifications (healthy clone → pass; older than timeout → fail; either way
// the clone is deleted), then start verifications for branches due under
// `every`. Requires a backup config — without an archive there is nothing to
// verify.
func (e *Engine) VerifyRestores(ctx context.Context, every, timeout time.Duration) error {
	if e.backup == nil {
		return nil
	}

	running, err := e.src.ListRunningRestoreVerifications(ctx)
	if err != nil {
		return fmt.Errorf("list running verifications: %w", err)
	}
	for _, v := range running {
		if err := e.settleVerification(ctx, v, timeout); err != nil {
			e.log.Error("settle restore verification", "branch", v.BranchID, "err", err)
		}
	}

	due, err := e.src.ListRestoreVerifyDue(ctx, every, 0)
	if err != nil {
		return fmt.Errorf("list due verifications: %w", err)
	}
	for _, w := range due {
		if err := e.startVerification(ctx, w); err != nil {
			e.log.Error("start restore verification", "branch", w.Branch.ID, "err", err)
		}
	}
	return nil
}

func (e *Engine) startVerification(ctx context.Context, w postgres.BranchWork) error {
	// Record first, then create: a crash between the two leaves a running row
	// with no cluster, which settles as a timeout fail — never a silent skip.
	if err := e.src.StartRestoreVerification(ctx, w.Branch.ID); err != nil {
		return err
	}
	clone := BuildRecoveryCluster(w, RecoveryTarget{}, e.backup,
		VerifyClusterName(w.Branch.ID), NamespaceName(w.ProjectID))
	if err := e.ensure(ctx, clone); err != nil {
		return err
	}
	e.log.Info("restore verification started", "branch", w.Branch.ID)
	return nil
}

func (e *Engine) settleVerification(ctx context.Context, v postgres.RestoreVerification, timeout time.Duration) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ClusterGVK)
	key := client.ObjectKey{Namespace: NamespaceName(v.ProjectID), Name: VerifyClusterName(v.BranchID)}
	err := e.kc.Get(ctx, key, u)
	switch {
	case err == nil:
		want, _, _ := unstructured.NestedInt64(u.Object, "spec", "instances")
		got, found, _ := unstructured.NestedInt64(u.Object, "status", "readyInstances")
		if found && want > 0 && got >= want {
			// The archive restored into a healthy cluster — that IS the proof.
			if err := e.src.FinishRestoreVerification(ctx, v.BranchID, true, ""); err != nil {
				return err
			}
			e.log.Info("restore verification passed", "branch", v.BranchID)
			return e.deleteVerifyCluster(ctx, u)
		}
	case !apierrors.IsNotFound(err):
		return err
		// NotFound: crash-window case (row written, cluster never created) —
		// fall through to the timeout check rather than failing instantly.
	}

	if time.Since(v.StartedAt) > timeout {
		msg := fmt.Sprintf("recovery clone not healthy within %s — archive may be unrestorable; page the operator (R-2)", timeout)
		if err := e.src.FinishRestoreVerification(ctx, v.BranchID, false, msg); err != nil {
			return err
		}
		e.log.Error("restore verification FAILED", "branch", v.BranchID, "timeout", timeout.String())
		if err == nil { // clone exists — tear it down
			return e.deleteVerifyCluster(ctx, u)
		}
	}
	return nil
}

func (e *Engine) deleteVerifyCluster(ctx context.Context, u *unstructured.Unstructured) error {
	if err := e.kc.Delete(ctx, u); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
