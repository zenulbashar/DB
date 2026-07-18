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
	FinishBranchTeardown(ctx context.Context, branchID string) error
	CountLiveBranches(ctx context.Context, projectID string) (int, error)
	ListRoutableEndpoints(ctx context.Context) ([]postgres.RoutableEndpoint, error)
}

type Engine struct {
	src Source
	kc  client.Client
	log *slog.Logger
}

func New(src Source, kc client.Client, log *slog.Logger) *Engine {
	return &Engine{src: src, kc: kc, log: log}
}

// Scheme returns a runtime.Scheme covering everything the engine touches —
// core/networking types plus the CNPG GVKs registered as unstructured.
func Scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	for _, gvk := range []schema.GroupVersionKind{ClusterGVK, PoolerGVK} {
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
		}
	}
	keep(e.publishRoutes(ctx))
	return firstErr
}

func (e *Engine) provision(ctx context.Context, w postgres.BranchWork) error {
	objs := []*unstructured.Unstructured{BuildNamespace(w), BuildResourceQuota(w)}
	objs = append(objs, BuildNetworkPolicies(w)...)
	objs = append(objs, BuildCluster(w), BuildPooler(w))
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

func (e *Engine) teardown(ctx context.Context, w postgres.BranchWork) error {
	ns := NamespaceName(w.ProjectID)
	for _, ref := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
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
	if !changed {
		return nil
	}
	return e.kc.Update(ctx, existing)
}
