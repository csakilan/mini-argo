package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
)

// enablePrune turns pruning on for an existing GitSource. It defaults to
// false, which is the behaviour most of these tests want to leave alone.
func enablePrune(key types.NamespacedName) {
	GinkgoHelper()
	var gs gitopsv1alpha1.GitSource
	Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
	gs.Spec.Prune = true
	Expect(k8sClient.Update(ctx, &gs)).To(Succeed())
}

func cmExists(name string) bool {
	GinkgoHelper()
	var cm corev1.ConfigMap
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: defaultNS}, &cm)
	if apierrors.IsNotFound(err) {
		return false
	}
	Expect(err).To(Succeed())
	return true
}

var _ = Describe("Inventory", func() {

	It("records everything it applied", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "a.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: inv-a\n  namespace: default\n")
		writeManifest(dir, "b.yaml", "apiVersion: v1\nkind: Service\nmetadata:\n  name: inv-b\n  namespace: default\nspec:\n  ports:\n    - port: 80\n")

		key := newGitSource("inventory-records", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		inv := statusOf(key).Inventory
		Expect(inv).To(HaveLen(2))
		Expect(inv[0].Kind).To(Equal("ConfigMap"))
		Expect(inv[0].Name).To(Equal("inv-a"))
		Expect(inv[0].Namespace).To(Equal(defaultNS))
		Expect(inv[1].Kind).To(Equal("Service"))
	})

	It("keeps the old inventory when a sync fails", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "a.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: inv-keep\n  namespace: default\n")

		key := newGitSource("inventory-keeps-on-failure", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())
		before := statusOf(key).Inventory
		Expect(before).To(HaveLen(1))

		var gs gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
		gs.Spec.Path = "gone"
		Expect(k8sClient.Update(ctx, &gs)).To(Succeed())
		Expect(reconcileOnce(dir, key)).To(Succeed())

		Expect(statusOf(key).Phase).To(Equal(PhaseError))
		Expect(statusOf(key).Inventory).To(Equal(before),
			"a partial inventory would tell the next pass to prune healthy objects")
	})
})

var _ = Describe("Pruning", func() {

	It("deletes an object removed from the source when prune is on", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "keep.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: prune-keep\n  namespace: default\n")
		writeManifest(dir, "gone.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: prune-gone\n  namespace: default\n")

		key := newGitSource("prune-enabled", defaultNS)
		enablePrune(key)
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(cmExists("prune-keep")).To(BeTrue())
		Expect(cmExists("prune-gone")).To(BeTrue())

		By("removing one manifest from the source")
		Expect(removeManifest(dir, "gone.yaml")).To(Succeed())
		Expect(reconcileOnce(dir, key)).To(Succeed())

		Expect(cmExists("prune-keep")).To(BeTrue())
		Expect(cmExists("prune-gone")).To(BeFalse())
		Expect(statusOf(key).Inventory).To(HaveLen(1))
		Expect(statusOf(key).Message).To(ContainSubstring("prune-gone"))
	})

	// The one that matters more. Deleting things by default would be
	// unforgivable.
	It("leaves the object alone when prune is off", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "keep.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: noprune-keep\n  namespace: default\n")
		writeManifest(dir, "gone.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: noprune-gone\n  namespace: default\n")

		key := newGitSource("prune-disabled", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(cmExists("noprune-gone")).To(BeTrue())

		Expect(removeManifest(dir, "gone.yaml")).To(Succeed())
		Expect(reconcileOnce(dir, key)).To(Succeed())

		Expect(cmExists("noprune-gone")).To(BeTrue(),
			"prune defaults to false and must stay that way")
		Expect(statusOf(key).Inventory).To(HaveLen(1),
			"the inventory still narrows, so enabling prune later cleans up")
	})

	It("refuses to prune an object another GitSource has taken over", func() {
		dirA := GinkgoT().TempDir()
		writeManifest(dirA, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: handover\n  namespace: default\n")
		keyA := newGitSource("handover-a", defaultNS)
		enablePrune(keyA)
		Expect(reconcileOnce(dirA, keyA)).To(Succeed())

		By("a second GitSource claiming the same object")
		dirB := GinkgoT().TempDir()
		writeManifest(dirB, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: handover\n  namespace: default\n")
		keyB := newGitSource("handover-b", defaultNS)
		Expect(reconcileOnce(dirB, keyB)).To(Succeed())

		By("the first one dropping it from its source")
		Expect(removeManifest(dirA, "cm.yaml")).To(Succeed())
		Expect(reconcileOnce(dirA, keyA)).To(Succeed())

		Expect(cmExists("handover")).To(BeTrue(),
			"the inventory said it was ours, but the label says otherwise")
	})

	It("does not prune when an apply failed in the same pass", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "a-good.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: partial-keep\n  namespace: default\n")
		writeManifest(dir, "b-doomed.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: partial-doomed\n  namespace: default\n")

		key := newGitSource("prune-partial-failure", defaultNS)
		enablePrune(key)
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(cmExists("partial-doomed")).To(BeTrue())

		By("removing one manifest and breaking another in the same pass")
		Expect(removeManifest(dir, "b-doomed.yaml")).To(Succeed())
		writeManifest(dir, "c-broken.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: broken\n  namespace: nowhere-at-all\n")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		Expect(statusOf(key).Phase).To(Equal(PhaseError))
		Expect(cmExists("partial-doomed")).To(BeTrue(),
			"a failed pass must never delete anything")
	})

	It("treats an already-deleted object as nothing to do", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: already-gone\n  namespace: default\n")

		key := newGitSource("prune-already-gone", defaultNS)
		enablePrune(key)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "already-gone", Namespace: defaultNS}, &cm)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cm)).To(Succeed())

		Expect(removeManifest(dir, "cm.yaml")).To(Succeed())
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(statusOf(key).Phase).To(Equal(PhaseSynced))
	})
})

var _ = Describe("The finalizer", func() {

	It("is added on the first reconcile", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fin-cm\n  namespace: default\n")

		key := newGitSource("finalizer-added", defaultNS)
		var before gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &before)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(&before, FinalizerName)).To(BeFalse())

		Expect(reconcileOnce(dir, key)).To(Succeed())

		var after gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &after)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(&after, FinalizerName)).To(BeTrue())
	})

	It("holds the object in Terminating until cleanup finishes", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: teardown-cm\n  namespace: default\n")

		key := newGitSource("finalizer-teardown", defaultNS)
		enablePrune(key)
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(cmExists("teardown-cm")).To(BeTrue())

		By("deleting the GitSource")
		var gs gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &gs)).To(Succeed())

		By("finding it still there, waiting")
		var terminating gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &terminating)).To(Succeed())
		Expect(terminating.DeletionTimestamp).NotTo(BeNil())
		Expect(cmExists("teardown-cm")).To(BeTrue(), "nothing cleaned up yet")

		By("reconciling, which cleans up and releases it")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		Expect(cmExists("teardown-cm")).To(BeFalse())
		err := k8sClient.Get(ctx, key, &gitopsv1alpha1.GitSource{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the GitSource should now be gone")
	})

	It("leaves objects running when prune is off, but still releases the object", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: survives-cm\n  namespace: default\n")

		key := newGitSource("finalizer-no-prune", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var gs gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &gs)).To(Succeed())
		Expect(reconcileOnce(dir, key)).To(Succeed())

		Expect(cmExists("survives-cm")).To(BeTrue(),
			"deleting a GitSource is not a request to delete the workloads")
		err := k8sClient.Get(ctx, key, &gitopsv1alpha1.GitSource{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "it must not hang in Terminating")
	})

	It("does not resurrect objects for a GitSource being deleted", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: no-resurrect\n  namespace: default\n")

		key := newGitSource("finalizer-no-resurrect", defaultNS)
		enablePrune(key)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var gs gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &gs)).To(Succeed())
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(cmExists("no-resurrect")).To(BeFalse())

		By("reconciling once more, as the queue may well do")
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(cmExists("no-resurrect")).To(BeFalse(), "a deleted GitSource must not re-apply")
	})

	It("still stamps ownership so pruning can verify it later", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: owner-check\n  namespace: default\n")

		key := newGitSource("owner-label", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "owner-check", Namespace: defaultNS}, &cm)).To(Succeed())
		Expect(cm.Labels).To(HaveKeyWithValue(OwnedByLabel, "default_owner-label"))
	})

})
