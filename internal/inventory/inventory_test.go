package inventory

import (
	"testing"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
)

const (
	kindDeployment = "Deployment"
	groupApps      = "apps"
	nsDemo         = "demo"
	nameWeb        = "web"
)

func ref(kind, ns, name string) gitopsv1alpha1.ResourceRef {
	return gitopsv1alpha1.ResourceRef{Version: "v1", Kind: kind, Namespace: ns, Name: name}
}

func names(refs []gitopsv1alpha1.ResourceRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Kind+"/"+r.Namespace+"/"+r.Name)
	}
	return out
}

func equal(got []gitopsv1alpha1.ResourceRef, want ...string) bool {
	g := names(got)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

func TestDiff_ReturnsRemovedRefs(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{
		ref("ConfigMap", nsDemo, "kept"),
		ref("ConfigMap", nsDemo, "removed"),
	}
	current := []gitopsv1alpha1.ResourceRef{
		ref("ConfigMap", nsDemo, "kept"),
	}

	got := Diff(previous, current)
	if !equal(got, "ConfigMap/demo/removed") {
		t.Fatalf("got %v, want [ConfigMap/demo/removed]", names(got))
	}
}

func TestDiff_EmptyWhenIdentical(t *testing.T) {
	refs := []gitopsv1alpha1.ResourceRef{
		ref("ConfigMap", nsDemo, "a"),
		ref("Service", nsDemo, "b"),
	}
	if got := Diff(refs, refs); len(got) != 0 {
		t.Fatalf("expected nothing to prune, got %v", names(got))
	}
}

// Objects added to the repository are created by the apply pass, not by this.
func TestDiff_IgnoresAdditions(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{ref("ConfigMap", nsDemo, "a")}
	current := []gitopsv1alpha1.ResourceRef{
		ref("ConfigMap", nsDemo, "a"),
		ref("ConfigMap", nsDemo, "b"),
	}
	if got := Diff(previous, current); len(got) != 0 {
		t.Fatalf("additions are not deletions, got %v", names(got))
	}
}

func TestDiff_EmptyPreviousPrunesNothing(t *testing.T) {
	current := []gitopsv1alpha1.ResourceRef{ref("ConfigMap", nsDemo, "a")}
	if got := Diff(nil, current); len(got) != 0 {
		t.Fatalf("a GitSource that has never synced owns nothing, got %v", names(got))
	}
}

// Emptying the repository is a legitimate way to remove everything.
func TestDiff_EmptyCurrentPrunesEverything(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{
		ref("ConfigMap", nsDemo, "a"),
		ref("Service", nsDemo, "b"),
	}
	got := Diff(previous, nil)
	if !equal(got, "ConfigMap/demo/a", "Service/demo/b") {
		t.Fatalf("got %v", names(got))
	}
}

func TestDiff_PreservesPreviousOrder(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{
		ref("ConfigMap", nsDemo, "z"),
		ref("ConfigMap", nsDemo, "a"),
		ref("ConfigMap", nsDemo, "m"),
	}
	got := Diff(previous, nil)
	if !equal(got, "ConfigMap/demo/z", "ConfigMap/demo/a", "ConfigMap/demo/m") {
		t.Fatalf("order should follow previous, got %v", names(got))
	}
}

func TestDiff_DeduplicatesPrevious(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{
		ref("ConfigMap", nsDemo, "a"),
		ref("ConfigMap", nsDemo, "a"),
	}
	got := Diff(previous, nil)
	if len(got) != 1 {
		t.Fatalf("expected one deletion, got %v", names(got))
	}
}

// Same name, different namespace, is a different object. Getting this wrong
// means either failing to delete something or deleting the wrong thing.
func TestDiff_NamespaceIsPartOfIdentity(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{ref("ConfigMap", "one", "shared")}
	current := []gitopsv1alpha1.ResourceRef{ref("ConfigMap", "two", "shared")}

	got := Diff(previous, current)
	if !equal(got, "ConfigMap/one/shared") {
		t.Fatalf("got %v", names(got))
	}
}

func TestDiff_KindIsPartOfIdentity(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{ref("ConfigMap", nsDemo, nameWeb)}
	current := []gitopsv1alpha1.ResourceRef{ref("Service", nsDemo, nameWeb)}

	got := Diff(previous, current)
	if !equal(got, "ConfigMap/demo/web") {
		t.Fatalf("got %v", names(got))
	}
}

func TestDiff_GroupIsPartOfIdentity(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{
		{Group: groupApps, Version: "v1", Kind: kindDeployment, Namespace: nsDemo, Name: nameWeb},
	}
	current := []gitopsv1alpha1.ResourceRef{
		{Group: "batch", Version: "v1", Kind: kindDeployment, Namespace: nsDemo, Name: nameWeb},
	}
	if got := Diff(previous, current); len(got) != 1 {
		t.Fatalf("different groups are different objects, got %v", names(got))
	}
}

// A version bump is the same object, not a deletion and a creation.
func TestDiff_VersionIsNotPartOfIdentity(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{
		{Group: groupApps, Version: "v1beta1", Kind: kindDeployment, Namespace: nsDemo, Name: nameWeb},
	}
	current := []gitopsv1alpha1.ResourceRef{
		{Group: groupApps, Version: "v1", Kind: kindDeployment, Namespace: nsDemo, Name: nameWeb},
	}
	if got := Diff(previous, current); len(got) != 0 {
		t.Fatalf("a moved apiVersion must not delete the object, got %v", names(got))
	}
}

func TestDiff_ClusterScopedObjects(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{ref("Namespace", "", "old")}
	current := []gitopsv1alpha1.ResourceRef{ref("Namespace", "", "new")}

	got := Diff(previous, current)
	if !equal(got, "Namespace//old") {
		t.Fatalf("got %v", names(got))
	}
}

func TestDiff_DoesNotMutateItsInputs(t *testing.T) {
	previous := []gitopsv1alpha1.ResourceRef{
		ref("ConfigMap", nsDemo, "a"),
		ref("ConfigMap", nsDemo, "b"),
	}
	current := []gitopsv1alpha1.ResourceRef{ref("ConfigMap", nsDemo, "a")}

	_ = Diff(previous, current)

	if len(previous) != 2 || len(current) != 1 {
		t.Fatal("Diff must not modify the slices it was given")
	}
}
