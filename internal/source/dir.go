package source

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
)

// DirFetcher reads manifests from a directory on local disk.
//
// It exists for two reasons. Tests use it so they never touch the network,
// and Phase 3 uses it so the apply path can be built before Git is involved.
type DirFetcher struct {
	// Root is the directory that spec.Path is resolved against. Nothing
	// outside it is readable.
	Root string
}

// compile-time check that DirFetcher satisfies the interface.
var _ Source = DirFetcher{}

// Fetch reads every .yaml and .yml file under Root/spec.Path, recursively.
func (d DirFetcher) Fetch(ctx context.Context, spec gitopsv1alpha1.GitSourceSpec) (Snapshot, error) {
	dir, err := d.resolve(spec.Path)
	if err != nil {
		return Snapshot{}, err
	}

	info, err := os.Stat(dir)
	if err != nil {
		return Snapshot{}, fmt.Errorf("reading path %q: %w", spec.Path, err)
	}
	if !info.IsDir() {
		return Snapshot{}, fmt.Errorf("path %q is a file, not a directory", spec.Path)
	}

	var paths []string
	err = filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".yaml", ".yml":
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("walking %q: %w", spec.Path, err)
	}

	// WalkDir already visits in lexical order, but sorting makes the guarantee
	// explicit and independent of that implementation detail. A stable order
	// matters because apply order is otherwise nondeterministic.
	sort.Strings(paths)

	snap := Snapshot{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return Snapshot{}, fmt.Errorf("reading %q: %w", p, err)
		}
		snap.Manifests = append(snap.Manifests, b)
	}

	// A directory has no revision. Empty is the honest answer.
	snap.Commit = ""
	return snap, nil
}

// resolve turns spec.Path into an absolute path and refuses anything that
// would escape Root. spec.Path comes from a user-editable object, so it is
// untrusted input.
func (d DirFetcher) resolve(specPath string) (string, error) {
	if specPath == "" {
		specPath = "."
	}
	if filepath.IsAbs(specPath) {
		return "", fmt.Errorf("path %q must be relative to the repository root", specPath)
	}

	root, err := filepath.Abs(d.Root)
	if err != nil {
		return "", fmt.Errorf("resolving root %q: %w", d.Root, err)
	}

	target := filepath.Clean(filepath.Join(root, specPath))

	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("resolving path %q: %w", specPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the repository root", specPath)
	}

	return target, nil
}
