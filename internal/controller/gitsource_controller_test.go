package controller

import (
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
	"github.com/csakilan/mini-argo/internal/source"
)

// writeManifest drops a YAML file into dir, creating parents as needed.
func writeManifest(dir, rel, content string) {
	GinkgoHelper()
	full := filepath.Join(dir, rel)
	Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
	Expect(os.WriteFile(full, []byte(content), 0o644)).To(Succeed())
}

// newGitSource creates a GitSource in the cluster and returns its key.
func newGitSource(name, namespace string) types.NamespacedName {
	GinkgoHelper()
	gs := &gitopsv1alpha1.GitSource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gitopsv1alpha1.GitSourceSpec{
			Repo:     "https://example.invalid/unused",
			Branch:   "main",
			Path:     ".",
			Interval: metav1.Duration{Duration: 0},
		},
	}
	Expect(k8sClient.Create(ctx, gs)).To(Succeed())
	key := types.NamespacedName{Name: name, Namespace: namespace}
	DeferCleanup(func() {
		_ = k8sClient.Delete(ctx, gs)
	})
	return key
}

// reconcileOnce runs a single pass with a DirFetcher rooted at dir.
func reconcileOnce(dir string, key types.NamespacedName) error {
	GinkgoHelper()
	r := &GitSourceReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
		Source: source.DirFetcher{Root: dir},
	}
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	return err
}

func statusOf(key types.NamespacedName) gitopsv1alpha1.GitSourceStatus {
	GinkgoHelper()
	var gs gitopsv1alpha1.GitSource
	Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
	return gs.Status
}

var _ = Describe("GitSource Controller: folder to cluster", func() {

	It("applies an object from a directory into the cluster", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: applied-cm
  namespace: default
data:
  greeting: hello
`)
		key := newGitSource("apply-one", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "applied-cm", Namespace: "default"}, &cm)).To(Succeed())
		Expect(cm.Data).To(HaveKeyWithValue("greeting", "hello"))
	})

	It("applies every document in a multi-document file", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "two.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: multi-first
  namespace: default
data:
  n: "1"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: multi-second
  namespace: default
data:
  n: "2"
---
`)
		key := newGitSource("apply-multi", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		for _, name := range []string{"multi-first", "multi-second"} {
			var cm corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &cm)).
				To(Succeed(), "expected %s to exist", name)
		}
	})

	It("reads files from subdirectories and ignores non-YAML", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "top.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: nested-top
  namespace: default
`)
		writeManifest(dir, "sub/inner.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: nested-inner
  namespace: default
`)
		writeManifest(dir, "README.md", "# not a manifest, and not valid YAML either: {[}\n")

		key := newGitSource("apply-nested", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		for _, name := range []string{"nested-top", "nested-inner"} {
			var cm corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &cm)).To(Succeed())
		}
		Expect(statusOf(key).Phase).To(Equal(PhaseSynced))
	})

	It("stamps the ownership label on everything it applies", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: labelled-cm
  namespace: default
`)
		key := newGitSource("apply-label", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "labelled-cm", Namespace: "default"}, &cm)).To(Succeed())
		Expect(cm.Labels).To(HaveKeyWithValue(OwnedByLabel, "default_apply-label"))
	})

	It("preserves labels already present in the manifest", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: keeps-labels
  namespace: default
  labels:
    team: platform
`)
		key := newGitSource("apply-keeps-labels", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "keeps-labels", Namespace: "default"}, &cm)).To(Succeed())
		Expect(cm.Labels).To(HaveKeyWithValue("team", "platform"))
		Expect(cm.Labels).To(HaveKey(OwnedByLabel))
	})

	It("puts a manifest with no namespace into the GitSource's namespace", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gs-home"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: no-namespace-cm
`)
		key := newGitSource("apply-default-ns", "gs-home")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "no-namespace-cm", Namespace: "gs-home"}, &cm)).
			To(Succeed(), "object should land in the GitSource's own namespace")
	})

	It("sets phase to Synced after a successful apply", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: status-cm
  namespace: default
`)
		key := newGitSource("status-synced", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		st := statusOf(key)
		Expect(st.Phase).To(Equal(PhaseSynced))
		Expect(st.Message).To(BeEmpty())
		Expect(st.LastSyncedCommit).To(BeEmpty(), "a directory has no revision")
	})

	It("reports Error and does not create the namespace when the target is missing", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: orphan-cm
  namespace: does-not-exist
`)
		key := newGitSource("missing-ns", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		st := statusOf(key)
		Expect(st.Phase).To(Equal(PhaseError))
		Expect(st.Message).To(ContainSubstring("orphan-cm"))

		var ns corev1.Namespace
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "does-not-exist"}, &ns)
		Expect(err).To(HaveOccurred(), "the controller must never create namespaces")
	})

	It("reports Error when the YAML is malformed", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "broken.yaml", "apiVersion: v1\nkind: ConfigMap\n\tbad: indentation\n")

		key := newGitSource("bad-yaml", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(statusOf(key).Phase).To(Equal(PhaseError))
	})

	It("reports Error when spec.path does not exist", func() {
		dir := GinkgoT().TempDir()
		key := newGitSource("bad-path", "default")

		var gs gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
		gs.Spec.Path = "no-such-directory"
		Expect(k8sClient.Update(ctx, &gs)).To(Succeed())

		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(statusOf(key).Phase).To(Equal(PhaseError))
	})

	It("is idempotent: applying twice changes nothing", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: idempotent-cm
  namespace: default
data:
  k: v
`)
		key := newGitSource("idempotent", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var first corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "idempotent-cm", Namespace: "default"}, &first)).To(Succeed())

		Expect(reconcileOnce(dir, key)).To(Succeed())

		var second corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "idempotent-cm", Namespace: "default"}, &second)).To(Succeed())
		Expect(second.ResourceVersion).To(Equal(first.ResourceVersion),
			"a second identical apply should not write anything")
	})

	It("applies cluster-scoped objects without a namespace", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "ns.yaml", `
apiVersion: v1
kind: Namespace
metadata:
  name: created-by-manifest
  namespace: this-should-be-stripped
`)
		key := newGitSource("cluster-scoped", "default")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var ns corev1.Namespace
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "created-by-manifest"}, &ns)).To(Succeed())
		Expect(ns.Namespace).To(BeEmpty())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, &ns) })
	})

	It("does nothing when the GitSource has been deleted", func() {
		dir := GinkgoT().TempDir()
		err := reconcileOnce(dir, types.NamespacedName{Name: "never-existed", Namespace: "default"})
		Expect(err).To(Succeed(), fmt.Sprintf("a missing object is not an error"))
	})
})
