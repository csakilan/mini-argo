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
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
	"github.com/csakilan/mini-argo/internal/drift"
	"github.com/csakilan/mini-argo/internal/inventory"
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

	// FinalizerName is what stops the API server deleting a GitSource before
	// this controller has had a chance to clean up after it. While it is
	// present, a delete request only sets deletionTimestamp and waits.
	FinalizerName = "gitops.miniargo.dev/finalizer"

	// DefaultInterval is used when spec.interval is missing or not positive.
	// A GitSource that never requeues would stop correcting drift entirely,
	// which is a quiet way for the whole tool to stop working.
	DefaultInterval = 30 * time.Second

	// maxReportedFields caps how many changed field paths go into
	// status.message, so one badly drifted object cannot fill it.
	maxReportedFields = 3
)

// Phases reported in status.phase. These must match the enum marker on the type.
const (
	PhaseSynced  = "Synced"
	PhaseDrifted = "Drifted"
	PhaseError   = "Error"
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

	// Being deleted. Clean up, then step out of the way.
	if !gs.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &gs)
	}

	// Claim the object before applying anything, so there is never a window
	// where objects exist and nothing is registered to clean them up.
	if !controllerutil.ContainsFinalizer(&gs, FinalizerName) {
		controllerutil.AddFinalizer(&gs, FinalizerName)
		if err := r.Update(ctx, &gs); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	snap, err := r.Source.Fetch(ctx, gs.Spec)
	if err != nil {
		log.Error(err, "fetch failed")
		return r.report(ctx, &gs, gitopsv1alpha1.GitSourceStatus{
			Phase:            PhaseError,
			Message:          fmt.Sprintf("fetching manifests: %v", err),
			LastSyncedCommit: gs.Status.LastSyncedCommit,
			LastSyncTime:     gs.Status.LastSyncTime,
			DriftedObjects:   gs.Status.DriftedObjects,
			Inventory:        gs.Status.Inventory,
		})
	}

	objs, err := manifests.Parse(snap.Manifests)
	if err != nil {
		log.Error(err, "parse failed")
		return r.report(ctx, &gs, gitopsv1alpha1.GitSourceStatus{
			Phase:            PhaseError,
			Message:          fmt.Sprintf("parsing manifests: %v", err),
			LastSyncedCommit: gs.Status.LastSyncedCommit,
			LastSyncTime:     gs.Status.LastSyncTime,
			DriftedObjects:   gs.Status.DriftedObjects,
			Inventory:        gs.Status.Inventory,
		})
	}

	var (
		failures  []string
		applied   []gitopsv1alpha1.ResourceRef
		drifted   []gitopsv1alpha1.ResourceRef
		summaries []string
	)
	for _, obj := range objs {
		result, err := r.applyObject(ctx, &gs, obj)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		applied = append(applied, result.ref)
		if result.corrected {
			drifted = append(drifted, result.ref)
			summaries = append(summaries, result.summary)
			log.Info("corrected drift", "object", result.summary)
		}
	}

	if len(failures) > 0 {
		log.Error(nil, "apply failed", "failures", len(failures), "total", len(objs))
		// Keep the previous commit, sync time, and inventory. This attempt did
		// not succeed, and recording a partial inventory would tell the next
		// pass to prune every object that merely failed to apply.
		return r.report(ctx, &gs, gitopsv1alpha1.GitSourceStatus{
			Phase:            PhaseError,
			Message:          strings.Join(failures, "; "),
			LastSyncedCommit: gs.Status.LastSyncedCommit,
			LastSyncTime:     gs.Status.LastSyncTime,
			Inventory:        gs.Status.Inventory,
		})
	}

	// Pruning only happens on a clean pass, for the reason above.
	var pruned []string
	if gs.Spec.Prune {
		for _, ref := range inventory.Diff(gs.Status.Inventory, applied) {
			deleted, err := r.pruneRef(ctx, &gs, ref)
			if err != nil {
				failures = append(failures, err.Error())
				continue
			}
			if deleted {
				pruned = append(pruned, fmt.Sprintf("%s/%s", ref.Kind, ref.Name))
				log.Info("pruned object removed from source", "kind", ref.Kind, "name", ref.Name, "namespace", ref.Namespace)
			}
		}
	}

	if len(failures) > 0 {
		return r.report(ctx, &gs, gitopsv1alpha1.GitSourceStatus{
			Phase:            PhaseError,
			Message:          strings.Join(failures, "; "),
			LastSyncedCommit: gs.Status.LastSyncedCommit,
			LastSyncTime:     gs.Status.LastSyncTime,
			Inventory:        gs.Status.Inventory,
		})
	}

	next := gitopsv1alpha1.GitSourceStatus{
		Phase:            PhaseSynced,
		LastSyncedCommit: snap.Commit,
		LastSyncTime:     &metav1.Time{Time: time.Now()},
		DriftedObjects:   drifted,
		Inventory:        applied,
	}
	switch {
	case len(drifted) > 0:
		next.Phase = PhaseDrifted
		next.Message = "corrected " + strings.Join(summaries, "; ")
	case len(pruned) > 0:
		next.Message = "pruned " + strings.Join(pruned, ", ")
	}

	log.Info("applied manifests",
		"objects", len(objs), "drifted", len(drifted), "pruned", len(pruned), "commit", snap.Commit)
	return r.report(ctx, &gs, next)
}

// finalize runs when a GitSource is being deleted.
//
// Pruning is opt-in, so a GitSource with prune disabled leaves its objects
// running. That is deliberate: deleting the GitSource is not a request to
// delete the workloads, and silently tearing down production because somebody
// removed a config object would be indefensible.
func (r *GitSourceReconciler) finalize(ctx context.Context, gs *gitopsv1alpha1.GitSource) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(gs, FinalizerName) {
		return ctrl.Result{}, nil
	}

	if gs.Spec.Prune {
		var failures []string
		for _, ref := range gs.Status.Inventory {
			if _, err := r.pruneRef(ctx, gs, ref); err != nil {
				failures = append(failures, err.Error())
			}
		}
		if len(failures) > 0 {
			// Keep the finalizer and try again. The object stays in a
			// terminating state rather than disappearing with objects orphaned.
			return ctrl.Result{}, fmt.Errorf("cleaning up: %s", strings.Join(failures, "; "))
		}
		log.Info("cleaned up owned objects", "count", len(gs.Status.Inventory))
	} else {
		log.Info("prune disabled, leaving owned objects in place", "count", len(gs.Status.Inventory))
	}

	controllerutil.RemoveFinalizer(gs, FinalizerName)
	if err := r.Update(ctx, gs); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// pruneRef deletes one object this GitSource used to own. It reports whether
// anything was actually deleted.
//
// The ownership label is re-checked immediately before deleting. The inventory
// alone is not proof: another GitSource may have taken the object over since,
// in which case it is no longer ours to remove.
func (r *GitSourceReconciler) pruneRef(ctx context.Context, gs *gitopsv1alpha1.GitSource, ref gitopsv1alpha1.ResourceRef) (bool, error) {
	log := logf.FromContext(ctx)

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(schema.GroupVersionKind{Group: ref.Group, Version: ref.Version, Kind: ref.Kind})

	err := r.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, live)
	switch {
	case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
		// Already gone, or its kind no longer exists. Nothing to do.
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reading %s/%s before pruning: %w", ref.Kind, ref.Name, err)
	}

	if owner := live.GetLabels()[OwnedByLabel]; owner != ownerValue(gs) {
		log.Info("refusing to prune an object owned by someone else",
			"kind", ref.Kind, "name", ref.Name, "owner", owner)
		return false, nil
	}

	if err := r.Delete(ctx, live); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("deleting %s/%s: %w", ref.Kind, ref.Name, err)
	}
	return true, nil
}

// ownerValue is the label value identifying one GitSource.
func ownerValue(gs *gitopsv1alpha1.GitSource) string {
	return fmt.Sprintf("%s_%s", gs.Namespace, gs.Name)
}

// requeueAfter is the clock that makes drift correction continuous.
//
// The controller watches GitSource objects and nothing else, so a manual
// change to an applied object produces no event it can hear. Waking up on a
// timer and looking is the only sensor it has.
func requeueAfter(gs *gitopsv1alpha1.GitSource) time.Duration {
	if gs.Spec.Interval.Duration <= 0 {
		return DefaultInterval
	}
	return gs.Spec.Interval.Duration
}

// applyOutcome records what happened to one object during a sync.
type applyOutcome struct {
	ref       gitopsv1alpha1.ResourceRef
	corrected bool
	summary   string
}

// applyObject stamps ownership onto one object, notes any drift, and
// server-side applies it.
//
// There is no "read it, compare it, patch the difference" branch anywhere in
// here. Apply declares that these fields should hold these values and that
// this field owner owns them, and the API server enforces that. Apply is the
// correction mechanism.
//
// The read below exists purely so drift can be *reported*. Delete it and the
// controller still corrects everything, silently. Detection and correction are
// two different jobs.
func (r *GitSourceReconciler) applyObject(ctx context.Context, gs *gitopsv1alpha1.GitSource, obj *unstructured.Unstructured) (applyOutcome, error) {
	name := fmt.Sprintf("%s %s", obj.GetKind(), obj.GetName())

	if err := r.defaultNamespace(gs, obj); err != nil {
		return applyOutcome{}, fmt.Errorf("%s: %w", name, err)
	}

	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[OwnedByLabel] = ownerValue(gs)
	obj.SetLabels(labels)

	out := applyOutcome{ref: refFor(obj)}

	corrected, err := r.detectDrift(ctx, obj)
	if err != nil {
		return applyOutcome{}, fmt.Errorf("%s: %w", name, err)
	}
	if corrected != nil {
		out.corrected = true
		out.summary = corrected.summary
	}

	if err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(obj),
		client.FieldOwner(FieldOwner),
		client.ForceOwnership); err != nil {
		return applyOutcome{}, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// driftSummary is the minimal shape detectDrift needs to report.
type driftSummary struct {
	summary string
}

// detectDrift reads the live object and compares it against the desired one.
// A missing object is not drift: that is a creation.
func (r *GitSourceReconciler) detectDrift(ctx context.Context, obj *unstructured.Unstructured) (*driftSummary, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(obj.GroupVersionKind())

	switch err := r.Get(ctx, client.ObjectKeyFromObject(obj), live); {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading live object: %w", err)
	}

	fields := drift.Fields(obj.Object, live.Object)
	if len(fields) == 0 {
		return nil, nil
	}

	return &driftSummary{
		summary: fmt.Sprintf("%s/%s (%s)", obj.GetKind(), obj.GetName(), drift.Summarise(fields, maxReportedFields)),
	}, nil
}

// refFor records enough to find an object again later, which Phase 6 needs in
// order to delete it.
func refFor(obj *unstructured.Unstructured) gitopsv1alpha1.ResourceRef {
	gvk := obj.GroupVersionKind()
	return gitopsv1alpha1.ResourceRef{
		Group:     gvk.Group,
		Version:   gvk.Version,
		Kind:      gvk.Kind,
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}
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

// report writes status when it has meaningfully changed, and always asks to be
// called again after the interval.
//
// The guard skips writes where nothing but the timestamp would move, which
// matters most when a GitSource is failing: a broken repository should not
// generate an API write every reconcile forever.
func (r *GitSourceReconciler) report(ctx context.Context, gs *gitopsv1alpha1.GitSource, next gitopsv1alpha1.GitSourceStatus) (ctrl.Result, error) {
	if statusMeaningfullyEqual(gs.Status, next) {
		return ctrl.Result{RequeueAfter: requeueAfter(gs)}, nil
	}

	gs.Status = next
	if err := r.Status().Update(ctx, gs); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return ctrl.Result{RequeueAfter: requeueAfter(gs)}, nil
}

// statusMeaningfullyEqual includes LastSyncTime, deliberately.
//
// A successful sync always carries a fresh timestamp, so it always writes, and
// the field means what its name says: when this last actually synced. A failed
// attempt passes the previous timestamp through unchanged, so a GitSource
// pointing at a broken repository settles into writing nothing at all rather
// than generating an API write every interval forever.
//
// Writing on every successful pass is only safe because the watch filters out
// status-only updates. Without that predicate this would be a hot loop.
func statusMeaningfullyEqual(a, b gitopsv1alpha1.GitSourceStatus) bool {
	return a.Phase == b.Phase &&
		a.Message == b.Message &&
		a.LastSyncedCommit == b.LastSyncedCommit &&
		a.LastSyncTime.Equal(b.LastSyncTime) &&
		slices.Equal(a.DriftedObjects, b.DriftedObjects) &&
		slices.Equal(a.Inventory, b.Inventory)
}

// SetupWithManager sets up the controller with the Manager.
func (r *GitSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Only react to changes in what the user asked for.
	//
	// Every successful sync writes status, and a status write produces a watch
	// event. Without this filter that event triggers another reconcile, which
	// writes status again, forever. Status writes do not bump
	// metadata.generation, so filtering on generation lets the interval be the
	// only thing that drives repeated syncs.
	// A delete request only sets deletionTimestamp while a finalizer is
	// present. If that update were filtered out, the finalizer would never be
	// removed and the object would sit in Terminating forever, so it is
	// allowed through explicitly rather than relying on generation changing.
	deleting := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return !e.ObjectNew.GetDeletionTimestamp().IsZero()
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&gitopsv1alpha1.GitSource{},
			builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, deleting))).
		Named("gitsource").
		Complete(r)
}
