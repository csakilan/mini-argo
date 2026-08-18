package source

import (
	"context"
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
	for i := 0; i < 3; i++ {
		got, err := DirFetcher{Root: root}.Fetch(context.Background(), specWithPath("."))
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		var kinds []string
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
