package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
)

// write creates a file, making parent directories as needed.
func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func specWithPath(p string) gitopsv1alpha1.GitSourceSpec {
	return gitopsv1alpha1.GitSourceSpec{Repo: "unused", Branch: "main", Path: p}
}

func TestDirFetcher_ReadsYAMLFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.yaml", "kind: A\n")
	write(t, root, "b.yaml", "kind: B\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 2 {
		t.Fatalf("expected 2 files, got %d", len(got.Manifests))
	}
}

func TestDirFetcher_AcceptsBothExtensions(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.yaml", "kind: A\n")
	write(t, root, "b.yml", "kind: B\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 2 {
		t.Fatalf("expected .yaml and .yml both read, got %d files", len(got.Manifests))
	}
}

func TestDirFetcher_IgnoresNonYAML(t *testing.T) {
	root := t.TempDir()
	write(t, root, "deploy.yaml", "kind: Deployment\n")
	write(t, root, "README.md", "# not a manifest\n")
	write(t, root, "notes.txt", "kind: Trap\n")
	write(t, root, "script.sh", "#!/bin/sh\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 1 {
		t.Fatalf("expected only the .yaml file, got %d", len(got.Manifests))
	}
	if !strings.Contains(string(got.Manifests[0]), "Deployment") {
		t.Errorf("read the wrong file: %q", got.Manifests[0])
	}
}

func TestDirFetcher_RespectsSpecPath(t *testing.T) {
	root := t.TempDir()
	write(t, root, "top.yaml", "kind: Top\n")
	write(t, root, "manifests/inner.yaml", "kind: Inner\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("manifests"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 1 {
		t.Fatalf("expected 1 file from the subdirectory, got %d", len(got.Manifests))
	}
	if !strings.Contains(string(got.Manifests[0]), "Inner") {
		t.Errorf("read outside spec.path: %q", got.Manifests[0])
	}
}

func TestDirFetcher_AcceptsDotSlashPrefix(t *testing.T) {
	root := t.TempDir()
	write(t, root, "manifests/inner.yaml", "kind: Inner\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("./manifests"))
	if err != nil {
		t.Fatalf("Fetch with ./ prefix: %v", err)
	}
	if len(got.Manifests) != 1 {
		t.Fatalf("expected 1 file, got %d", len(got.Manifests))
	}
}

func TestDirFetcher_RecursesSubdirectories(t *testing.T) {
	root := t.TempDir()
	write(t, root, "one.yaml", "kind: One\n")
	write(t, root, "sub/two.yaml", "kind: Two\n")
	write(t, root, "sub/deeper/three.yaml", "kind: Three\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 3 {
		t.Fatalf("expected 3 files across subdirectories, got %d", len(got.Manifests))
	}
}

func TestDirFetcher_DeterministicOrder(t *testing.T) {
	root := t.TempDir()
	write(t, root, "z.yaml", "kind: Z\n")
	write(t, root, "a.yaml", "kind: A\n")
	write(t, root, "m.yaml", "kind: M\n")

	var first []string
	for i := range 3 {
		got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		kinds := make([]string, 0, len(got.Manifests))
		for _, m := range got.Manifests {
			kinds = append(kinds, strings.TrimSpace(string(m)))
		}
		if i == 0 {
			first = kinds
			continue
		}
		if strings.Join(kinds, ",") != strings.Join(first, ",") {
			t.Fatalf("order changed between runs: %v then %v", first, kinds)
		}
	}
	if first[0] != "kind: A" {
		t.Errorf("expected alphabetical order, got %v", first)
	}
}

func TestDirFetcher_ReturnsEmptyCommit(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.yaml", "kind: A\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Commit != "" {
		t.Errorf("a directory has no revision, want empty Commit, got %q", got.Commit)
	}
}

func TestDirFetcher_EmptyDirectoryIsNotAnError(t *testing.T) {
	root := t.TempDir()

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("an empty directory should not be an error: %v", err)
	}
	if len(got.Manifests) != 0 {
		t.Fatalf("expected 0 manifests, got %d", len(got.Manifests))
	}
}

func TestDirFetcher_ErrorsOnMissingPath(t *testing.T) {
	root := t.TempDir()

	_, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("nope"))
	if err == nil {
		t.Fatal("expected an error for a path that does not exist, got nil")
	}
}

func TestDirFetcher_ErrorsWhenPathIsAFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "single.yaml", "kind: A\n")

	_, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("single.yaml"))
	if err == nil {
		t.Fatal("expected an error when spec.path is a file rather than a directory")
	}
}

// spec.path comes from a user-editable object. It must not be able to read
// files outside the configured root.
func TestDirFetcher_RejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	write(t, root, "inside/ok.yaml", "kind: Inside\n")
	write(t, root, "secret.yaml", "kind: Secret\n")

	for _, p := range []string{"..", "../", "inside/../..", "/etc"} {
		t.Run(p, func(t *testing.T) {
			_, err := DirFetcher{Root: filepath.Join(root, "inside")}.
				Fetch(context.Background(), specWithPath(p))
			if err == nil {
				t.Fatalf("path %q escaped the root without an error", p)
			}
		})
	}
}

// --- gap-closing tests added during Phase 3 review ---

func TestDirFetcher_UppercaseExtension(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.YAML", "kind: A\n")
	write(t, root, "b.Yml", "kind: B\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 2 {
		t.Fatalf("extension matching should be case-insensitive, got %d files", len(got.Manifests))
	}
}

func TestDirFetcher_EmptyFileIsRead(t *testing.T) {
	root := t.TempDir()
	write(t, root, "empty.yaml", "")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("an empty file should not be an error: %v", err)
	}
	if len(got.Manifests) != 1 {
		t.Fatalf("expected the empty file to be returned, got %d", len(got.Manifests))
	}
	if len(got.Manifests[0]) != 0 {
		t.Errorf("expected empty content, got %q", got.Manifests[0])
	}
}

func TestDirFetcher_TrailingSlashInPath(t *testing.T) {
	root := t.TempDir()
	write(t, root, "manifests/a.yaml", "kind: A\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("manifests/"))
	if err != nil {
		t.Fatalf("a trailing slash should be tolerated: %v", err)
	}
	if len(got.Manifests) != 1 {
		t.Fatalf("expected 1 file, got %d", len(got.Manifests))
	}
}

func TestDirFetcher_MissingRootIsAnError(t *testing.T) {
	_, err := DirFetcher{Root: "/definitely/not/a/real/directory"}.
		Fetch(context.Background(), specWithPath("."))
	if err == nil {
		t.Fatal("expected an error when Root does not exist")
	}
}

func TestDirFetcher_HiddenFilesAreRead(t *testing.T) {
	// Documents current behaviour rather than asserting it is right.
	// Real GitOps tools differ on whether dotfiles should be applied.
	root := t.TempDir()
	write(t, root, ".hidden.yaml", "kind: Hidden\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 1 {
		t.Fatalf("expected hidden files to be read, got %d", len(got.Manifests))
	}
}

// A .yaml symlink pointing outside the root would let spec.path reach files it
// should not. WalkDir does not descend symlinked directories, but os.ReadFile
// follows symlinked files.
func TestDirFetcher_DoesNotFollowSymlinkedFilesOutsideRoot(t *testing.T) {
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.yaml")
	if err := os.WriteFile(secretPath, []byte("kind: Secret\nstolen: true\n"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	root := t.TempDir()
	if err := os.Symlink(secretPath, filepath.Join(root, "innocent.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		return // refusing outright is an acceptable outcome
	}
	for _, m := range got.Manifests {
		if strings.Contains(string(m), "stolen") {
			t.Fatalf("read a file outside the root through a symlink: %q", m)
		}
	}
}

func TestDirFetcher_DoesNotDescendSymlinkedDirsOutsideRoot(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "secret.yaml", "kind: Secret\nstolen: true\n")

	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		return
	}
	for _, m := range got.Manifests {
		if strings.Contains(string(m), "stolen") {
			t.Fatalf("descended a symlinked directory outside the root: %q", m)
		}
	}
}

func TestDirFetcher_HandlesManyFiles(t *testing.T) {
	root := t.TempDir()
	const n = 250
	for i := range n {
		write(t, root, fmt.Sprintf("f%03d.yaml", i), fmt.Sprintf("kind: K%d\n", i))
	}

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != n {
		t.Fatalf("expected %d files, got %d", n, len(got.Manifests))
	}
}

func TestDirFetcher_RespectsContextCancellation(t *testing.T) {
	root := t.TempDir()
	for i := range 50 {
		write(t, root, fmt.Sprintf("f%02d.yaml", i), "kind: K\n")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := DirFetcher{Root: root}.Fetch(ctx, specWithPath("."))
	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
}

func TestDirFetcher_EscapingSymlinkIsALoudError(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.yaml")
	if err := os.WriteFile(secret, []byte("kind: Secret\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	root := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(root, "innocent.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err == nil {
		t.Fatal("expected an error, not a silent skip: an operator needs to know")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error should say it was a symlink, got: %v", err)
	}
}

func TestDirFetcher_SymlinkInsideRootIsFollowed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "real/config.yaml", "kind: Real\n")
	if err := os.Symlink(filepath.Join(root, "real", "config.yaml"), filepath.Join(root, "alias.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("a symlink inside the root is legitimate: %v", err)
	}
	if len(got.Manifests) != 2 {
		t.Fatalf("expected both the real file and the alias, got %d", len(got.Manifests))
	}
}

func TestDirFetcher_SkipsGitDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "real.yaml", "kind: Real\n")
	write(t, root, ".git/config.yaml", "kind: NotAManifest\n")
	write(t, root, ".git/refs/heads/thing.yaml", "kind: AlsoNot\n")

	got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Manifests) != 1 {
		t.Fatalf("expected only the real manifest, got %d", len(got.Manifests))
	}
	if !strings.Contains(string(got.Manifests[0]), "Real") {
		t.Errorf("read something from .git: %q", got.Manifests[0])
	}
}
