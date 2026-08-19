package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
)

// initRepo creates a real git repository on disk with "main" as its default
// branch. Tests clone from these local paths, so nothing ever touches the
// network.
func initRepo(t *testing.T) (string, *git.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	return dir, repo
}

func commitFile(t *testing.T, repo *git.Repository, dir, rel, content string) plumbing.Hash {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(rel); err != nil {
		t.Fatalf("add %s: %v", rel, err)
	}
	hash, err := wt.Commit("add "+rel, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return hash
}

func gitSpec(repo, branch, path string) gitopsv1alpha1.GitSourceSpec {
	return gitopsv1alpha1.GitSourceSpec{Repo: repo, Branch: branch, Path: path}
}

func localFetcher() GitFetcher {
	return GitFetcher{AllowLocalPaths: true}
}

func TestGitFetcher_ClonesAndReadsFiles(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "a.yaml", "kind: A\n")
	commitFile(t, repo, dir, "b.yaml", "kind: B\n")

	got, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 2 {
		t.Fatalf("expected 2 manifests, got %d", len(got.Manifests))
	}
}

func TestGitFetcher_ReturnsShortHeadSHA(t *testing.T) {
	dir, repo := initRepo(t)
	hash := commitFile(t, repo, dir, "a.yaml", "kind: A\n")

	got, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := hash.String()[:7]
	if got.Commit != want {
		t.Fatalf("Commit = %q, want %q (short form of %s)", got.Commit, want, hash)
	}
}

func TestGitFetcher_RespectsSpecPath(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "top.yaml", "kind: Top\n")
	commitFile(t, repo, dir, "manifests/inner.yaml", "kind: Inner\n")

	got, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "manifests"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 1 || !strings.Contains(string(got.Manifests[0]), "Inner") {
		t.Fatalf("expected only the subdirectory, got %d files", len(got.Manifests))
	}
}

func TestGitFetcher_RespectsBranch(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "a.yaml", "kind: OnMain\n")

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("staging"),
		Create: true,
	}); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	commitFile(t, repo, dir, "a.yaml", "kind: OnStaging\n")

	main, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "."))
	if err != nil {
		t.Fatalf("fetch main: %v", err)
	}
	if !strings.Contains(string(main.Manifests[0]), "OnMain") {
		t.Errorf("main branch gave %q", main.Manifests[0])
	}

	staging, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "staging", "."))
	if err != nil {
		t.Fatalf("fetch staging: %v", err)
	}
	if !strings.Contains(string(staging.Manifests[0]), "OnStaging") {
		t.Errorf("staging branch gave %q", staging.Manifests[0])
	}
	if main.Commit == staging.Commit {
		t.Error("two branches should report different commits")
	}
}

func TestGitFetcher_PicksUpNewCommits(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "a.yaml", "kind: First\n")

	before, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "."))
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	commitFile(t, repo, dir, "a.yaml", "kind: Second\n")

	after, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "."))
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	if before.Commit == after.Commit {
		t.Fatal("commit should have changed after a new commit")
	}
	if !strings.Contains(string(after.Manifests[0]), "Second") {
		t.Errorf("expected the new content, got %q", after.Manifests[0])
	}
}

func TestGitFetcher_ErrorsOnMissingBranch(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "a.yaml", "kind: A\n")

	_, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "no-such-branch", "."))
	if err == nil {
		t.Fatal("expected an error for a branch that does not exist")
	}
}

func TestGitFetcher_ErrorsOnMissingRepo(t *testing.T) {
	_, err := localFetcher().Fetch(context.Background(),
		gitSpec(filepath.Join(t.TempDir(), "nope"), "main", "."))
	if err == nil {
		t.Fatal("expected an error for a repository that does not exist")
	}
}

func TestGitFetcher_ErrorsOnMissingSpecPath(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "a.yaml", "kind: A\n")

	_, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "not-here"))
	if err == nil {
		t.Fatal("expected an error for a path missing from the repository")
	}
}

// spec.repo is user input. Only public HTTPS is supported, so anything that
// would reach for credentials or the local filesystem must be refused.
func TestGitFetcher_RejectsNonHTTPSSchemes(t *testing.T) {
	for _, url := range []string{
		"git@github.com:someone/repo.git",
		"ssh://git@github.com/someone/repo.git",
		"git://github.com/someone/repo.git",
		"file:///etc",
		"/etc/passwd",
		"../../../etc",
	} {
		t.Run(url, func(t *testing.T) {
			_, err := GitFetcher{}.Fetch(context.Background(), gitSpec(url, "main", "."))
			if err == nil {
				t.Fatalf("scheme in %q should have been refused", url)
			}
			// Every refusal must give the same actionable message, rather
			// than leaking a URL parser error for scp-style URLs.
			if !strings.Contains(err.Error(), "must be a public https URL") {
				t.Errorf("unhelpful message for %q: %v", url, err)
			}
		})
	}
}

func TestGitFetcher_AcceptsHTTPSURLs(t *testing.T) {
	// Refused for being unreachable, not for its scheme.
	_, err := GitFetcher{}.Fetch(context.Background(),
		gitSpec("https://example.invalid/nope.git", "main", "."))
	if err == nil {
		t.Fatal("expected a network error")
	}
	if strings.Contains(err.Error(), "must be a public https") {
		t.Errorf("https URL was rejected on scheme: %v", err)
	}
}

// A repository is untrusted input, and anyone can commit a symlink to it.
func TestGitFetcher_RefusesSymlinkEscapingTheClone(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "ok.yaml", "kind: OK\n")

	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "sneaky.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	wt, _ := repo.Worktree()
	if _, err := wt.Add("sneaky.yaml"); err != nil {
		t.Fatalf("add symlink: %v", err)
	}
	if _, err := wt.Commit("add symlink", &git.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@example.invalid", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit symlink: %v", err)
	}

	got, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "."))
	if err != nil {
		if !strings.Contains(err.Error(), "symlink") {
			t.Errorf("expected the error to mention a symlink, got: %v", err)
		}
		return
	}
	for _, m := range got.Manifests {
		if strings.Contains(string(m), "localhost") {
			t.Fatal("read /etc/hosts through a symlink committed to the repository")
		}
	}
}

func TestGitFetcher_LeavesNoTemporaryDirectoriesBehind(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "a.yaml", "kind: A\n")

	work := t.TempDir()
	f := GitFetcher{WorkDir: work, AllowLocalPaths: true}

	for range 3 {
		if _, err := f.Fetch(context.Background(), gitSpec(dir, "main", ".")); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	}

	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected the clone directory to be cleaned up, found %d entries", len(entries))
	}
}

func TestGitFetcher_RespectsContextCancellation(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "a.yaml", "kind: A\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := localFetcher().Fetch(ctx, gitSpec(dir, "main", ".")); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
}

func TestGitFetcher_IgnoresNonYAMLInTheRepository(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "deploy.yaml", "kind: Deployment\n")
	commitFile(t, repo, dir, "README.md", "# docs, not manifests\n")

	got, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 1 {
		t.Fatalf("expected only the YAML file, got %d", len(got.Manifests))
	}
}

// The .git directory is full of files, none of which are manifests.
func TestGitFetcher_DoesNotReadTheGitDirectory(t *testing.T) {
	dir, repo := initRepo(t)
	commitFile(t, repo, dir, "a.yaml", "kind: A\n")

	got, err := localFetcher().Fetch(context.Background(), gitSpec(dir, "main", "."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %d: something under .git was read", len(got.Manifests))
	}
}

func TestGitFetcher_SatisfiesSourceInterface(t *testing.T) {
	var _ Source = GitFetcher{}
}
