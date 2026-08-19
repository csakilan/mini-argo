// Package inventory works out what a GitSource used to own and no longer does.
//
// This is the whole basis for deleting anything safely. The naive rule,
// "delete whatever is in the cluster but not in Git", would have the controller
// destroy objects it never created, including cluster components. The rule
// here is narrower and defensible: take back only what you brought.
package inventory

import gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"

// identity is what makes two references the same object.
//
// Version is deliberately excluded. A manifest moving from apps/v1beta1 to
// apps/v1 is the same Deployment with a newer API version, and treating that
// as a deletion followed by a creation would tear down a running workload for
// no reason.
type identity struct {
	group     string
	kind      string
	namespace string
	name      string
}

func identityOf(r gitopsv1alpha1.ResourceRef) identity {
	return identity{group: r.Group, kind: r.Kind, namespace: r.Namespace, name: r.Name}
}

// Diff returns the references in previous that are absent from current, in the
// order they appeared in previous, with duplicates removed.
//
// Read it as: objects this GitSource applied last time and is not applying
// now. Anything only in current is a new object, which the apply pass creates;
// deletion is not involved.
func Diff(previous, current []gitopsv1alpha1.ResourceRef) []gitopsv1alpha1.ResourceRef {
	stillWanted := make(map[identity]struct{}, len(current))
	for _, r := range current {
		stillWanted[identityOf(r)] = struct{}{}
	}

	seen := make(map[identity]struct{}, len(previous))
	var removed []gitopsv1alpha1.ResourceRef

	for _, r := range previous {
		id := identityOf(r)
		if _, wanted := stillWanted[id]; wanted {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		removed = append(removed, r)
	}

	return removed
}
