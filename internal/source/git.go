package source

import (
	"context"
	"fmt"
	"net/url"
	"os"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
)

// shortSHALength matches what `git log --oneline` prints, and what fits in a
// kubectl column.
const shortSHALength = 7

// GitFetcher reads manifests from a public Git repository.
//
// go-git is used rather than shelling out to the git binary, because it is
// pure Go. The Phase 8 container image therefore needs no git installed, which
// keeps the image small and removes a moving part from the build.
type GitFetcher struct {
	// WorkDir is where clones are made. Empty means the system temp directory.
	WorkDir string

	// AllowLocalPaths permits cloning from a filesystem path instead of an
	// https URL. Tests set this so they can clone from a repository built in
	// a temp directory and never touch the network. Production must not.
	AllowLocalPaths bool
}

var _ Source = GitFetcher{}

// Fetch clones the repository and reads manifests out of it.
//
// The clone is shallow, single-branch, and thrown away afterwards. That means
// re-cloning on every sync, which is wasteful and is the honest simple thing:
// caching a clone per GitSource means owning a mutable directory, invalidating
// it when the branch or URL changes, and handling a corrupted one. Real tools
// do that work. This one does not.
func (g GitFetcher) Fetch(ctx context.Context, spec gitopsv1alpha1.GitSourceSpec) (Snapshot, error) {
	if err := g.validateRepo(spec.Repo); err != nil {
		return Snapshot{}, err
	}

	branch := spec.Branch
	if branch == "" {
		branch = "main"
	}

	dir, err := os.MkdirTemp(g.WorkDir, "miniargo-clone-")
	if err != nil {
		return Snapshot{}, fmt.Errorf("creating clone directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:           spec.Repo,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         1,
		Tags:          git.NoTags,
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("cloning %s at branch %q: %w", spec.Repo, branch, err)
	}

	head, err := repo.Head()
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolving HEAD of %s: %w", spec.Repo, err)
	}

	// Reading the files is exactly the job DirFetcher already does, including
	// refusing symlinks that point outside the tree. A repository is untrusted
	// input and anyone can commit a symlink to one, so that check earns its
	// keep here rather than being theoretical.
	snap, err := DirFetcher{Root: dir}.Fetch(ctx, spec)
	if err != nil {
		return Snapshot{}, err
	}

	snap.Commit = shortSHA(head.Hash())
	return snap, nil
}

func shortSHA(h plumbing.Hash) string {
	s := h.String()
	if len(s) <= shortSHALength {
		return s
	}
	return s[:shortSHALength]
}

// validateRepo refuses anything that is not a plain http(s) URL.
//
// spec.repo comes from a user-editable object. An ssh:// or scp-style URL
// would send go-git looking for credentials and known_hosts on the controller
// host, and a filesystem path would let a GitSource read local directories.
// Neither is in scope: this tool supports public repositories only.
func (g GitFetcher) validateRepo(raw string) error {
	if g.AllowLocalPaths {
		return nil
	}
	if raw == "" {
		return fmt.Errorf("spec.repo is empty")
	}

	// url.Parse errors outright on scp-style URLs such as
	// git@github.com:owner/repo.git, so both branches give the same guidance
	// rather than leaking a parser message at whoever wrote the YAML.
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w", errNotPublicHTTPS(raw))
	}
	switch u.Scheme {
	case "https", "http":
		if u.Host == "" {
			return fmt.Errorf("repo %q has no host", raw)
		}
		if u.User != nil {
			return fmt.Errorf("repo %q must not contain credentials", raw)
		}
		return nil
	default:
		return errNotPublicHTTPS(raw)
	}
}

func errNotPublicHTTPS(raw string) error {
	return fmt.Errorf("repo %q must be a public https URL: ssh, git, file, and local paths are not supported", raw)
}
