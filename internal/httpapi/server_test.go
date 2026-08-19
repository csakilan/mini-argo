package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
)

const (
	kindDeployment = "Deployment"
	nameWeb        = "web"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := gitopsv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func gitSource(name, ns string, mutate func(*gitopsv1alpha1.GitSource)) *gitopsv1alpha1.GitSource {
	gs := &gitopsv1alpha1.GitSource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: gitopsv1alpha1.GitSourceSpec{
			Repo:     "https://github.com/example/config",
			Branch:   "main",
			Path:     "./manifests",
			Interval: metav1.Duration{Duration: 30 * time.Second},
		},
	}
	if mutate != nil {
		mutate(gs)
	}
	return gs
}

func newServer(t *testing.T, objs ...runtime.Object) *Server {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRuntimeObjects(objs...).Build()
	return &Server{Client: c}
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) []GitSourceView {
	t.Helper()
	var out []GitSourceView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestGitSources_ReturnsEmptyArrayNotNull(t *testing.T) {
	rec := get(t, newServer(t), "/api/gitsources")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// A JSON null would make the UI branch on it. An empty array does not.
	if got := rec.Body.String(); got != "[]\n" && got != "[]" {
		t.Fatalf("body = %q, want an empty array", got)
	}
}

func TestGitSources_ReturnsSpecAndStatus(t *testing.T) {
	when := metav1.NewTime(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	gs := gitSource("demo-app", "default", func(g *gitopsv1alpha1.GitSource) {
		g.Spec.Prune = true
		g.Status = gitopsv1alpha1.GitSourceStatus{
			Phase:            "Synced",
			LastSyncedCommit: "a1b2c3d",
			LastSyncTime:     &when,
			Inventory: []gitopsv1alpha1.ResourceRef{
				{Group: "apps", Version: "v1", Kind: kindDeployment, Namespace: "demo", Name: nameWeb},
			},
		}
	})

	views := decode(t, get(t, newServer(t, gs), "/api/gitsources"))
	if len(views) != 1 {
		t.Fatalf("expected 1 GitSource, got %d", len(views))
	}
	v := views[0]

	if v.Name != "demo-app" || v.Namespace != "default" {
		t.Errorf("identity wrong: %+v", v)
	}
	if v.Repo != "https://github.com/example/config" || v.Branch != "main" || v.Path != "./manifests" {
		t.Errorf("spec wrong: %+v", v)
	}
	if v.Interval != "30s" {
		t.Errorf("interval = %q, want 30s", v.Interval)
	}
	if !v.Prune {
		t.Error("prune should be true")
	}
	if v.Phase != "Synced" || v.LastSyncedCommit != "a1b2c3d" {
		t.Errorf("status wrong: %+v", v)
	}
	if v.LastSyncTime == nil || !v.LastSyncTime.Equal(&when) {
		t.Errorf("lastSyncTime = %v", v.LastSyncTime)
	}
	if len(v.Inventory) != 1 || v.Inventory[0].Kind != kindDeployment {
		t.Errorf("inventory wrong: %+v", v.Inventory)
	}
}

func TestGitSources_ReportsDriftedObjects(t *testing.T) {
	gs := gitSource("drifting", "default", func(g *gitopsv1alpha1.GitSource) {
		g.Status.Phase = "Drifted"
		g.Status.Message = "corrected Deployment/web (spec.replicas)"
		g.Status.DriftedObjects = []gitopsv1alpha1.ResourceRef{
			{Group: "apps", Version: "v1", Kind: kindDeployment, Namespace: "demo", Name: nameWeb},
		}
	})

	v := decode(t, get(t, newServer(t, gs), "/api/gitsources"))[0]
	if v.Phase != "Drifted" {
		t.Errorf("phase = %q", v.Phase)
	}
	if len(v.DriftedObjects) != 1 || v.DriftedObjects[0].Name != nameWeb {
		t.Errorf("driftedObjects wrong: %+v", v.DriftedObjects)
	}
	if v.Message == "" {
		t.Error("message should be carried through so the UI can explain itself")
	}
}

func TestGitSources_PhaseDefaultsToPendingBeforeFirstSync(t *testing.T) {
	v := decode(t, get(t, newServer(t, gitSource("fresh", "default", nil)), "/api/gitsources"))[0]
	if v.Phase != "Pending" {
		t.Errorf("phase = %q, want Pending for a GitSource that has never synced", v.Phase)
	}
}

func TestGitSources_SortedForStableRendering(t *testing.T) {
	objs := []runtime.Object{
		gitSource("zulu", "default", nil),
		gitSource("alpha", "production", nil),
		gitSource("alpha", "default", nil),
	}
	views := decode(t, get(t, newServer(t, objs...), "/api/gitsources"))

	want := []string{"default/alpha", "default/zulu", "production/alpha"}
	for i, w := range want {
		got := views[i].Namespace + "/" + views[i].Name
		if got != w {
			t.Errorf("row %d = %q, want %q (rows must not jump around between polls)", i, got, w)
		}
	}
}

func TestGitSources_ListsAcrossNamespaces(t *testing.T) {
	views := decode(t, get(t, newServer(t,
		gitSource("one", "default", nil),
		gitSource("two", "other", nil),
	), "/api/gitsources"))

	if len(views) != 2 {
		t.Fatalf("expected GitSources from every namespace, got %d", len(views))
	}
}

func TestGitSources_SetsJSONContentType(t *testing.T) {
	rec := get(t, newServer(t), "/api/gitsources")
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// Read-only means read-only. Anything that could change the cluster is refused.
func TestGitSources_RejectsWriteMethods(t *testing.T) {
	s := newServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.handler().ServeHTTP(rec, httptest.NewRequest(method, "/api/gitsources", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s returned %d, want 405", method, rec.Code)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	rec := get(t, newServer(t), "/healthz")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	rec := get(t, newServer(t), "/api/nonsense")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestServer_StartServesAndShutsDown(t *testing.T) {
	s := newServer(t, gitSource("live", "default", nil))
	s.Addr = "127.0.0.1:0"

	// Listen first so the test knows the port, then hand it the same address.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	var resp *http.Response
	for range 200 {
		resp, err = http.Get("http://" + addr + "/healthz")
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("server never came up: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down when the context was cancelled")
	}
}

func TestServer_StartFailsOnABadAddress(t *testing.T) {
	s := newServer(t)
	s.Addr = "not-an-address"
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("expected an error for an unusable address")
	}
}

func TestServer_ServesTheUIWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>status</h1>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := newServer(t)
	s.UIDir = dir

	rec := get(t, s, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "status") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestServer_WithoutUIDirTheRootIs404(t *testing.T) {
	if rec := get(t, newServer(t), "/"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no UI is configured", rec.Code)
	}
}
