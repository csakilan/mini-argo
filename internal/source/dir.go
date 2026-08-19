package source

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

	// Containment checks for symlink targets need the root with symlinks
	// already resolved, otherwise a legitimate path fails the comparison.
	realRoot, err := filepath.EvalSymlinks(d.Root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolving root %q: %w", d.Root, err)
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
			// A clone puts an entire repository database inside the tree we
			// are about to walk. None of it is manifests.
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		switch strings.ToLower(filepath.Ext(p)) {
		case ".yaml", ".yml":
		default:
			return nil
		}

		// WalkDir uses lstat, so a symlinked directory arrives here as a
		// non-directory and is never descended into. A symlinked *file*
		// however would be followed by os.ReadFile, which is how a
		// repository could point at anything on the host.
		if entry.Type()&fs.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(p)
			if err != nil {
				return fmt.Errorf("resolving symlink %q: %w", p, err)
			}
			if !within(realRoot, target) {
				return fmt.Errorf("%q is a symlink pointing outside the source root", p)
			}
			info, err := os.Stat(target)
			if err != nil {
				return fmt.Errorf("reading symlink target %q: %w", p, err)
			}
			if info.IsDir() {
				// Consistent with WalkDir: symlinked directories are not
				// traversed.
				return nil
			}
		}

		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("walking %q: %w", spec.Path, err)
	}

	// WalkDir already visits in lexical order, but sorting makes the guarantee
	// explicit and independent of that implementation detail. A stable order
	// matters because apply order is otherwise nondeterministic.
	slices.Sort(paths)

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

	if !within(root, target) {
		return "", fmt.Errorf("path %q escapes the repository root", specPath)
	}

	return target, nil
}

// within reports whether target is root itself or sits underneath it.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
