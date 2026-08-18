/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
	"github.com/csakilan/mini-argo/internal/manifests"
	"github.com/csakilan/mini-argo/internal/source"
)

const (
	// FieldOwner identifies this controller to the API server's field
	// ownership tracking. It must never change: a different owner string
	// orphans every field this controller currently owns, and drift
	// correction silently stops working.
	FieldOwner = "mini-argo"

	// OwnedByLabel is stamped on every object applied, so it is possible to
	// see at a glance which GitSource put an object there.
	OwnedByLabel = "miniargo.dev/owned-by"
)

// Phases reported in status.phase. These must match the enum marker on the type.
const (
	PhaseSynced = "Synced"
	PhaseError  = "Error"
)

// GitSourceReconciler reconciles a GitSource object
type GitSourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Source is where manifests come from. The reconcile loop deliberately
	// does not know whether that is a directory or a Git repository.
	Source source.Source
}

// +kubebuilder:rbac:groups=gitops.miniargo.dev,resources=gitsources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gitops.miniargo.dev,resources=gitsources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gitops.miniargo.dev,resources=gitsources/finalizers,verbs=update

// A GitOps controller applies whatever the repository contains, so it cannot
// know in advance which types it needs permission for. This is deliberately
// broad, and is the honest cost of a general-purpose applier.
// +kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch;create;update;patch;delete

// Reconcile fetches manifests, parses them, and applies them to the cluster.
func (r *GitSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var gs gitopsv1alpha1.GitSource
	if err := r.Get(ctx, req.NamespacedName, &gs); err != nil {
		// An object that is already gone is not an error, it is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	snap, err := r.Source.Fetch(ctx, gs.Spec)
	if err != nil {
		log.Error(err, "fetch failed")
		return r.setStatus(ctx, &gs, PhaseError, fmt.Sprintf("fetching manifests: %v", err), gs.Status.LastSyncedCommit)
	}

	objs, err := manifests.Parse(snap.Manifests)
	if err != nil {
		log.Error(err, "parse failed")
		return r.setStatus(ctx, &gs, PhaseError, fmt.Sprintf("parsing manifests: %v", err), gs.Status.LastSyncedCommit)
	}

	var failures []string
	for _, obj := range objs {
		if err := r.applyObject(ctx, &gs, obj); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		log.Error(nil, "apply failed", "failures", len(failures), "total", len(objs))
		return r.setStatus(ctx, &gs, PhaseError, strings.Join(failures, "; "), gs.Status.LastSyncedCommit)
	}

	log.Info("applied manifests", "objects", len(objs), "commit", snap.Commit)
	return r.setStatus(ctx, &gs, PhaseSynced, "", snap.Commit)
}

// applyObject stamps ownership onto one object and server-side applies it.
//
// There is no "read it, compare it, patch the difference" branch anywhere in
// here. Apply declares that these fields should hold these values and that
// this field owner owns them, and the API server enforces that. Apply is the
// correction mechanism.
func (r *GitSourceReconciler) applyObject(ctx context.Context, gs *gitopsv1alpha1.GitSource, obj *unstructured.Unstructured) error {
	ref := fmt.Sprintf("%s %s", obj.GetKind(), obj.GetName())

	if err := r.defaultNamespace(gs, obj); err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}

	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[OwnedByLabel] = fmt.Sprintf("%s_%s", gs.Namespace, gs.Name)
	obj.SetLabels(labels)

	if err := r.Patch(ctx, obj, client.Apply,
		client.FieldOwner(FieldOwner),
		client.ForceOwnership); err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	return nil
}

// defaultNamespace puts a namespaced object without a namespace into the
// GitSource's own namespace, matching what `kubectl apply -f` does.
//
// It deliberately never creates a namespace. If the target does not exist the
// apply fails and that is reported in status. ArgoCD offers CreateNamespace=true
// for this, and auto-creating namespaces is how a GitOps tool quietly sprawls
// across a cluster.
func (r *GitSourceReconciler) defaultNamespace(gs *gitopsv1alpha1.GitSource, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()

	mapping, err := r.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("no such kind %q in this cluster: %w", gvk.String(), err)
	}

	switch mapping.Scope.Name() {
	case meta.RESTScopeNameNamespace:
		if obj.GetNamespace() == "" {
			obj.SetNamespace(gs.Namespace)
		}
	case meta.RESTScopeNameRoot:
		// Cluster-scoped objects must not carry a namespace at all.
		obj.SetNamespace("")
	}
	return nil
}

// setStatus writes status only when a value actually changed.
//
// This guard is not an optimisation. The controller watches GitSource objects,
// so an unconditional status write produces a watch event, which triggers
// another reconcile, which writes status again: an infinite hot loop.
func (r *GitSourceReconciler) setStatus(ctx context.Context, gs *gitopsv1alpha1.GitSource, phase, message, commit string) (ctrl.Result, error) {
	if gs.Status.Phase == phase &&
		gs.Status.Message == message &&
		gs.Status.LastSyncedCommit == commit {
		return ctrl.Result{}, nil
	}

	gs.Status.Phase = phase
	gs.Status.Message = message
	gs.Status.LastSyncedCommit = commit

	if err := r.Status().Update(ctx, gs); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GitSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gitopsv1alpha1.GitSource{}).
		Named("gitsource").
		Complete(r)
}
