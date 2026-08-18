// Package source fetches a snapshot of manifests from somewhere.
//
// The reconcile loop must not know or care where manifests come from. That is
// why fetching sits behind an interface: tests use a local directory and never
// touch the network, while production clones a Git repository, and the
// controller cannot tell the difference.
package source

import (
	"context"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
)

// Snapshot is everything one fetch produced.
type Snapshot struct {
	// Commit identifies the revision these manifests came from. A directory
	// has no revision, so DirFetcher returns "". That empty string is
	// meaningful rather than a stub: it means "this source cannot tell you".
	Commit string

	// Manifests holds the raw bytes of each file read, in a stable order.
	// A single entry may contain several YAML documents.
	Manifests [][]byte
}

// Source fetches manifests described by a GitSource's spec.
type Source interface {
	Fetch(ctx context.Context, spec gitopsv1alpha1.GitSourceSpec) (Snapshot, error)
}
