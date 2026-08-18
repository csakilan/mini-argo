package controller

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
	"github.com/csakilan/mini-argo/internal/source"
)

// defaultNS is the namespace most of these tests use.
const defaultNS = "default"

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
		key := newGitSource("apply-one", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "applied-cm", Namespace: defaultNS}, &cm)).To(Succeed())
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
		key := newGitSource("apply-multi", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		for _, name := range []string{"multi-first", "multi-second"} {
			var cm corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: defaultNS}, &cm)).
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

		key := newGitSource("apply-nested", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		for _, name := range []string{"nested-top", "nested-inner"} {
			var cm corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: defaultNS}, &cm)).To(Succeed())
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
		key := newGitSource("apply-label", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "labelled-cm", Namespace: defaultNS}, &cm)).To(Succeed())
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
		key := newGitSource("apply-keeps-labels", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "keeps-labels", Namespace: defaultNS}, &cm)).To(Succeed())
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
		key := newGitSource("status-synced", defaultNS)
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
		key := newGitSource("missing-ns", defaultNS)
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

		key := newGitSource("bad-yaml", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(statusOf(key).Phase).To(Equal(PhaseError))
	})

	It("reports Error when spec.path does not exist", func() {
		dir := GinkgoT().TempDir()
		key := newGitSource("bad-path", defaultNS)

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
		key := newGitSource("idempotent", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var first corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "idempotent-cm", Namespace: defaultNS}, &first)).To(Succeed())

		Expect(reconcileOnce(dir, key)).To(Succeed())

		var second corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "idempotent-cm", Namespace: defaultNS}, &second)).To(Succeed())
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
		key := newGitSource("cluster-scoped", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var ns corev1.Namespace
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "created-by-manifest"}, &ns)).To(Succeed())
		Expect(ns.Namespace).To(BeEmpty())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, &ns) })
	})

	It("does nothing when the GitSource has been deleted", func() {
		dir := GinkgoT().TempDir()
		err := reconcileOnce(dir, types.NamespacedName{Name: "never-existed", Namespace: defaultNS})
		Expect(err).To(Succeed(), "a missing object is not an error")
	})
})

var _ = Describe("GitSource Controller: review-phase edge cases", func() {

	It("overwrites a manual edit on the next apply", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: drift-preview
  namespace: default
data:
  replicas: "2"
`)
		key := newGitSource("drift-preview-gs", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		// Somebody edits it by hand, as a different field manager.
		var cm corev1.ConfigMap
		cmKey := types.NamespacedName{Name: "drift-preview", Namespace: defaultNS}
		Expect(k8sClient.Get(ctx, cmKey, &cm)).To(Succeed())
		cm.Data["replicas"] = "5"
		Expect(k8sClient.Update(ctx, &cm, client.FieldOwner("some-human"))).To(Succeed())

		Expect(k8sClient.Get(ctx, cmKey, &cm)).To(Succeed())
		Expect(cm.Data).To(HaveKeyWithValue("replicas", "5"), "the manual edit should have landed")

		// Apply again. No comparison logic exists anywhere; apply is the fix.
		Expect(reconcileOnce(dir, key)).To(Succeed())

		Expect(k8sClient.Get(ctx, cmKey, &cm)).To(Succeed())
		Expect(cm.Data).To(HaveKeyWithValue("replicas", "2"),
			"server-side apply should have taken the field back")
	})

	It("applies the last of two documents describing the same object", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "dupes.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: duplicated
  namespace: default
data:
  which: first
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: duplicated
  namespace: default
data:
  which: second
`)
		key := newGitSource("duplicate-objects", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "duplicated", Namespace: defaultNS}, &cm)).To(Succeed())
		Expect(cm.Data).To(HaveKeyWithValue("which", "second"),
			"a repository containing the same object twice silently applies the last one")
	})

	// Both GitSources use the same FieldOwner constant, so the API server
	// cannot tell them apart. Only the ownership label distinguishes them,
	// which matters when Phase 6 starts deleting things.
	It("lets two GitSources overwrite each other without a conflict", func() {
		dirA := GinkgoT().TempDir()
		writeManifest(dirA, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: contested
  namespace: default
data:
  owner: a
`)
		dirB := GinkgoT().TempDir()
		writeManifest(dirB, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: contested
  namespace: default
data:
  owner: b
`)
		keyA := newGitSource("contender-a", defaultNS)
		keyB := newGitSource("contender-b", defaultNS)

		Expect(reconcileOnce(dirA, keyA)).To(Succeed())
		Expect(reconcileOnce(dirB, keyB)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "contested", Namespace: defaultNS}, &cm)).To(Succeed())
		Expect(cm.Data).To(HaveKeyWithValue("owner", "b"))
		Expect(cm.Labels).To(HaveKeyWithValue(OwnedByLabel, "default_contender-b"),
			"the label records the most recent writer, and the previous one is not told")

		Expect(statusOf(keyA).Phase).To(Equal(PhaseSynced))
		Expect(statusOf(keyB).Phase).To(Equal(PhaseSynced))
	})

	It("writes no status at all when nothing changed", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: quiet-cm
  namespace: default
`)
		key := newGitSource("status-guard", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var before gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &before)).To(Succeed())

		for range 3 {
			Expect(reconcileOnce(dir, key)).To(Succeed())
		}

		var after gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &after)).To(Succeed())
		Expect(after.ResourceVersion).To(Equal(before.ResourceVersion),
			"an unguarded status write would loop the controller forever")
	})

	It("reports an unknown kind rather than crashing", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "alien.yaml", `
apiVersion: nonexistent.example.com/v1
kind: Alien
metadata:
  name: et
  namespace: default
`)
		key := newGitSource("unknown-kind", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		st := statusOf(key)
		Expect(st.Phase).To(Equal(PhaseError))
		Expect(st.Message).To(ContainSubstring("Alien"))
	})

	It("applies the objects it can when one of them fails", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "good.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: partial-good
  namespace: default
`)
		writeManifest(dir, "zbad.yaml", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: partial-bad
  namespace: nowhere-at-all
`)
		key := newGitSource("partial-failure", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "partial-good", Namespace: defaultNS}, &cm)).
			To(Succeed(), "one bad object must not block the others")
		Expect(statusOf(key).Phase).To(Equal(PhaseError))
	})
})
