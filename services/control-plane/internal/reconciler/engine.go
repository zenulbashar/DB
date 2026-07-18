package reconciler

import (
	"context"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

// Source is the desired-state side of the reconciler (implemented by the
// postgres store; faked in tests).
type Source interface {
	ListReconcileWork(ctx context.Context) ([]postgres.BranchWork, error)
	MarkBranchReady(ctx context.Context, branchID string) error
	MarkBranchSuspended(ctx context.Context, branchID string) error
	MarkBranchResumed(ctx context.Context, branchID string) error
	FinishBranchTeardown(ctx context.Context, branchID string) error
	CountLiveBranches(ctx context.Context, projectID string) (int, error)
	ListRoutableEndpoints(ctx context.Context) ([]postgres.RoutableEndpoint, error)
}

type Engine struct {
	src    Source
	kc     client.Client
	backup *BackupConfig // nil disables archiving/backups (local dev only)
	log    *slog.Logger
}

func New(src Source, kc client.Client, backup *BackupConfig, log *slog.Logger) *Engine {
	return &Engine{src: src, kc: kc, backup: backup, log: log}
}

// Scheme returns a runtime.Scheme covering everything the engine touches —
// core/networking types plus the CNPG GVKs registered as unstructured.
func Scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	for _, gvk := range []schema.GroupVersionKind{ClusterGVK, PoolerGVK, ScheduledBackupGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	return s
}

// ReconcileOnce runs a single convergence pass. It is idempotent and safe to
// re-run after any partial failure — the crash-safety contract every phase
// gate re-verifies (MASTER plan §7 "Everything reconciles").
func (e *Engine) ReconcileOnce(ctx context.Context) error {
	work, err := e.src.ListReconcileWork(ctx)
	if err != nil {
		return fmt.Errorf("list work: %w", err)
	}
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, w := range work {
		switch w.Branch.State {
		case domain.StateProvisioning:
			keep(e.provision(ctx, w))
		case domain.StateDeleting:
			keep(e.teardown(ctx, w))
		case domain.StateSuspending:
			keep(e.suspend(ctx, w))
		case domain.StateResuming:
			keep(e.resume(ctx, w))
		}
	}
	keep(e.publishRoutes(ctx))
	return firstErr
}

func (e *Engine) provision(ctx context.Context, w postgres.BranchWork) error {
	objs := []*unstructured.Unstructured{BuildNamespace(w), BuildResourceQuota(w)}
	objs = append(objs, BuildNetworkPolicies(w)...)
	objs = append(objs, BuildCluster(w, e.backup), BuildPooler(w))
	if e.backup != nil {
		objs = append(objs, BuildScheduledBackup(w))
	}
	for _, o := range objs {
		if err := e.ensure(ctx, o); err != nil {
			return fmt.Errorf("ensure %s/%s: %w", o.GetKind(), o.GetName(), err)
		}
	}
	ready, err := e.clusterReady(ctx, w)
	if err != nil {
		return err
	}
	if ready {
		if err := e.src.MarkBranchReady(ctx, w.Branch.ID); err != nil {
			return err
		}
		e.log.Info("branch ready", "branch", w.Branch.ID, "project", w.ProjectID)
	}
	return nil
}

// suspend converges a branch toward hibernated compute (ADR-014): it re-applies
// the Cluster (now carrying the cnpg.io/hibernation=on annotation) and the
// Pooler (scaled to zero) — both driven off w.Branch.State == suspending in the
// builders — then, once CNPG reports no ready instances, flips the branch to
// suspended. Idempotent: a re-run before hibernation completes simply re-checks.
// The data-bearing PVCs are retained; teardown (which deletes the Cluster) is
// never on this path.
func (e *Engine) suspend(ctx context.Context, w postgres.BranchWork) error {
	for _, o := range []*unstructured.Unstructured{BuildCluster(w, e.backup), BuildPooler(w)} {
		if err := e.ensure(ctx, o); err != nil {
			return fmt.Errorf("ensure %s/%s: %w", o.GetKind(), o.GetName(), err)
		}
	}
	hibernated, err := e.clusterHibernated(ctx, w)
	if err != nil {
		return err
	}
	if hibernated {
		if err := e.src.MarkBranchSuspended(ctx, w.Branch.ID); err != nil {
			return err
		}
		e.log.Info("branch suspended", "branch", w.Branch.ID, "project", w.ProjectID)
	}
	return nil
}

// resume converges a suspended branch back to running: it re-applies the
// Cluster (hibernation=off) and the Pooler (scaled back up) — both driven off
// w.Branch.State == resuming — then, once CNPG reports the cluster ready, flips
// the branch to ready. Mirrors provision's ready gate exactly.
func (e *Engine) resume(ctx context.Context, w postgres.BranchWork) error {
	for _, o := range []*unstructured.Unstructured{BuildCluster(w, e.backup), BuildPooler(w)} {
		if err := e.ensure(ctx, o); err != nil {
			return fmt.Errorf("ensure %s/%s: %w", o.GetKind(), o.GetName(), err)
		}
	}
	ready, err := e.clusterReady(ctx, w)
	if err != nil {
		return err
	}
	if ready {
		if err := e.src.MarkBranchResumed(ctx, w.Branch.ID); err != nil {
			return err
		}
		e.log.Info("branch resumed", "branch", w.Branch.ID, "project", w.ProjectID)
	}
	return nil
}

func (e *Engine) teardown(ctx context.Context, w postgres.BranchWork) error {
	ns := NamespaceName(w.ProjectID)
	for _, ref := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{ScheduledBackupGVK, ClusterName(w.Branch.ID) + "-nightly"},
		{PoolerGVK, PoolerName(w.Branch.ID)},
		{ClusterGVK, ClusterName(w.Branch.ID)},
	} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(ref.gvk)
		u.SetNamespace(ns)
		u.SetName(ref.name)
		if err := e.kc.Delete(ctx, u); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %s/%s: %w", ref.gvk.Kind, ref.name, err)
		}
	}
	live, err := e.src.CountLiveBranches(ctx, w.ProjectID)
	if err != nil {
		return err
	}
	if live == 0 {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"})
		u.SetName(ns)
		if err := e.kc.Delete(ctx, u); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete namespace %s: %w", ns, err)
		}
	}
	if err := e.src.FinishBranchTeardown(ctx, w.Branch.ID); err != nil {
		return err
	}
	e.log.Info("branch torn down", "branch", w.Branch.ID, "project", w.ProjectID)
	return nil
}

func (e *Engine) publishRoutes(ctx context.Context) error {
	eps, err := e.src.ListRoutableEndpoints(ctx)
	if err != nil {
		return err
	}
	routesJSON, err := BuildRoutesJSON(eps)
	if err != nil {
		return err
	}
	return e.ensure(ctx, BuildRoutesConfigMap(routesJSON))
}

// clusterReady checks CNPG's observed status: readyInstances covering
// spec.instances.
func (e *Engine) clusterReady(ctx context.Context, w postgres.BranchWork) (bool, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ClusterGVK)
	if err := e.kc.Get(ctx, client.ObjectKey{Namespace: NamespaceName(w.ProjectID), Name: ClusterName(w.Branch.ID)}, u); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	want, _, _ := unstructured.NestedInt64(u.Object, "spec", "instances")
	got, found, _ := unstructured.NestedInt64(u.Object, "status", "readyInstances")
	return found && want > 0 && got >= want, nil
}

// clusterHibernated reports whether CNPG has finished hibernating: the Cluster
// exists with a REPORTED zero ready instances. Suspend keeps spec.instances at
// the role value and toggles the hibernation annotation, so "hibernated" is
// observed as readyInstances dropping to 0 — not as a spec change (which is why
// clusterReady's want>0 guard correctly refuses to call a hibernated cluster
// "ready").
//
// The `found` guard is load-bearing (audit finding): CNPG omits
// status.readyInstances until it has reported instance status, so an absent
// field would otherwise read as 0 and mark the branch suspended before the
// compute has actually been observed down — the mirror of the mistake
// clusterReady already avoids. We only trust an OBSERVED zero.
func (e *Engine) clusterHibernated(ctx context.Context, w postgres.BranchWork) (bool, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ClusterGVK)
	if err := e.kc.Get(ctx, client.ObjectKey{Namespace: NamespaceName(w.ProjectID), Name: ClusterName(w.Branch.ID)}, u); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil // nothing left running
		}
		return false, err
	}
	got, found, _ := unstructured.NestedInt64(u.Object, "status", "readyInstances")
	return found && got == 0, nil
}

// ensure creates the object or updates its desired fields (spec/data/labels)
// in place, preserving cluster-populated metadata.
func (e *Engine) ensure(ctx context.Context, desired *unstructured.Unstructured) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	err := e.kc.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		return e.kc.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	changed := false
	for _, field := range []string{"spec", "data"} {
		if v, ok := desired.Object[field]; ok {
			existing.Object[field] = v
			changed = true
		}
	}
	if labels := desired.GetLabels(); len(labels) > 0 {
		existing.SetLabels(labels)
		changed = true
	}
	// Merge (not replace) annotations: the hibernation toggle lives here, but
	// CNPG and Kubernetes also write their own operator-managed annotations, so
	// we must set only our keys and preserve theirs.
	if anns := desired.GetAnnotations(); len(anns) > 0 {
		merged := existing.GetAnnotations()
		if merged == nil {
			merged = map[string]string{}
		}
		for k, v := range anns {
			merged[k] = v
		}
		existing.SetAnnotations(merged)
		changed = true
	}
	if !changed {
		return nil
	}
	return e.kc.Update(ctx, existing)
}
