// Package httpapi serves a small read-only view of GitSources for the status
// page.
//
// It lives inside the controller binary rather than in a separate service. The
// controller already holds an authenticated, cached client and a manager that
// can run extra servers, so a second deployment would duplicate all of that in
// order to serve three fields. It is also strictly read-only: the page reports
// what the controller did, and every change still goes through Git.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
)

// PhasePending is what a GitSource that has never synced reports. The
// controller never writes it, because it only writes status after doing work,
// so the API fills it in rather than showing the page an empty string.
const PhasePending = "Pending"

// ResourceRefView is one object a GitSource touched.
type ResourceRefView struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// GitSourceView is one row on the page: what was asked for, and what happened.
type GitSourceView struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`

	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	Path     string `json:"path"`
	Interval string `json:"interval"`
	Prune    bool   `json:"prune"`

	Phase            string       `json:"phase"`
	Message          string       `json:"message"`
	LastSyncedCommit string       `json:"lastSyncedCommit"`
	LastSyncTime     *metav1.Time `json:"lastSyncTime"`

	DriftedObjects []ResourceRefView `json:"driftedObjects"`
	Inventory      []ResourceRefView `json:"inventory"`
}

// Server exposes the read-only API, and optionally the built status page.
type Server struct {
	// Client reads GitSources. In the controller this is the manager's
	// cached client, so listing is a map lookup rather than an API call.
	Client client.Reader

	// Addr is the address to listen on, for example ":8082".
	Addr string

	// UIDir, when set, is a directory of built static files served at /.
	// Empty means API only, which is what `make run` uses while Vite serves
	// the page in development.
	UIDir string
}

// Start runs the server until ctx is cancelled. It satisfies
// manager.Runnable, so the manager owns its lifecycle.
func (s *Server) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("httpapi")

	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	log.Info("Starting status API", "addr", listener.Addr().String(), "ui", s.UIDir != "")

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/gitsources", s.listGitSources)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	if s.UIDir != "" {
		mux.Handle("/", spaHandler(s.UIDir))
	}

	return mux
}

func (s *Server) listGitSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "read-only API", http.StatusMethodNotAllowed)
		return
	}

	var list gitopsv1alpha1.GitSourceList
	if err := s.Client.List(r.Context(), &list); err != nil {
		logf.FromContext(r.Context()).Error(err, "listing GitSources")
		http.Error(w, "listing GitSources failed", http.StatusInternalServerError)
		return
	}

	// Always an array, never null: a null would make every consumer of this
	// endpoint write a special case for "no GitSources yet".
	views := make([]GitSourceView, 0, len(list.Items))
	for i := range list.Items {
		views = append(views, viewOf(&list.Items[i]))
	}

	// Stable order, so rows do not jump around between polls.
	slices.SortFunc(views, func(a, b GitSourceView) int {
		if n := strings.Compare(a.Namespace, b.Namespace); n != 0 {
			return n
		}
		return strings.Compare(a.Name, b.Name)
	})

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(views); err != nil {
		logf.FromContext(r.Context()).Error(err, "encoding response")
	}
}

func viewOf(gs *gitopsv1alpha1.GitSource) GitSourceView {
	phase := gs.Status.Phase
	if phase == "" {
		phase = PhasePending
	}

	return GitSourceView{
		Name:             gs.Name,
		Namespace:        gs.Namespace,
		Repo:             gs.Spec.Repo,
		Branch:           gs.Spec.Branch,
		Path:             gs.Spec.Path,
		Interval:         gs.Spec.Interval.Duration.String(),
		Prune:            gs.Spec.Prune,
		Phase:            phase,
		Message:          gs.Status.Message,
		LastSyncedCommit: gs.Status.LastSyncedCommit,
		LastSyncTime:     gs.Status.LastSyncTime,
		DriftedObjects:   refViews(gs.Status.DriftedObjects),
		Inventory:        refViews(gs.Status.Inventory),
	}
}

func refViews(refs []gitopsv1alpha1.ResourceRef) []ResourceRefView {
	out := make([]ResourceRefView, 0, len(refs))
	for _, r := range refs {
		out = append(out, ResourceRefView{
			Group: r.Group, Version: r.Version, Kind: r.Kind,
			Namespace: r.Namespace, Name: r.Name,
		})
	}
	return out
}

// spaHandler serves built static files, falling back to index.html so that a
// deep link refreshed in the browser does not 404.
func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		files.ServeHTTP(w, r)
	})
}
