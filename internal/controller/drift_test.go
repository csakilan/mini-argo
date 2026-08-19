package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gitopsv1alpha1 "github.com/csakilan/mini-argo/api/v1alpha1"
	"github.com/csakilan/mini-argo/internal/source"
)

// This is the product specification, written as a test.
//
//	given  a GitSource has synced a Deployment with 2 replicas
//	when   somebody scales it to 5 behind the controller's back
//	then   the next reconcile puts it back to 2
//	 and   status.driftedObjects names that Deployment
var _ = Describe("The drift demo", func() {

	const deploymentYAML = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: default
spec:
  replicas: 2
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: web
          image: nginx:1.29-alpine
`

	It("reverts a manual scale and reports what it corrected", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "deployment.yaml", deploymentYAML)

		key := newGitSource("drift-demo", defaultNS)
		deployKey := types.NamespacedName{Name: "web", Namespace: defaultNS}

		By("syncing it once, so the Deployment exists at 2 replicas")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, deployKey, &deploy)).To(Succeed())
		Expect(*deploy.Spec.Replicas).To(BeEquivalentTo(2))
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, &deploy) })

		By("having a human scale it to 5 behind the controller's back")
		deploy.Spec.Replicas = ptr(int32(5))
		Expect(k8sClient.Update(ctx, &deploy, client.FieldOwner("a-human-with-kubectl"))).To(Succeed())

		Expect(k8sClient.Get(ctx, deployKey, &deploy)).To(Succeed())
		Expect(*deploy.Spec.Replicas).To(BeEquivalentTo(5), "the manual change should have landed")

		By("reconciling again")
		Expect(reconcileOnce(dir, key)).To(Succeed())

		By("finding it back at 2")
		Expect(k8sClient.Get(ctx, deployKey, &deploy)).To(Succeed())
		Expect(*deploy.Spec.Replicas).To(BeEquivalentTo(2))

		By("and finding the correction reported in status")
		st := statusOf(key)
		Expect(st.DriftedObjects).To(HaveLen(1))
		Expect(st.DriftedObjects[0].Kind).To(Equal("Deployment"))
		Expect(st.DriftedObjects[0].Name).To(Equal("web"))
		Expect(st.DriftedObjects[0].Namespace).To(Equal(defaultNS))
		Expect(st.Phase).To(Equal(PhaseDrifted))
	})
})

func ptr[T any](v T) *T { return &v }

var _ = Describe("The interval", func() {

	// reconcileWithResult is like reconcileOnce but hands back the Result,
	// which is where the requeue lives.
	reconcileWithResult := func(dir string, key types.NamespacedName) reconcile.Result {
		GinkgoHelper()
		r := &GitSourceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			Source: source.DirFetcher{Root: dir},
		}
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(Succeed())
		return res
	}

	It("asks to be called again after spec.interval", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: interval-cm\n  namespace: default\n")

		key := newGitSource("interval-honoured", defaultNS)
		var gs gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
		gs.Spec.Interval = metav1.Duration{Duration: 45 * time.Second}
		Expect(k8sClient.Update(ctx, &gs)).To(Succeed())

		res := reconcileWithResult(dir, key)
		Expect(res.RequeueAfter).To(Equal(45 * time.Second))
	})

	It("still requeues after an error, so a broken source can recover", func() {
		dir := GinkgoT().TempDir()
		key := newGitSource("interval-on-error", defaultNS)

		var gs gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
		gs.Spec.Path = "does-not-exist"
		gs.Spec.Interval = metav1.Duration{Duration: 10 * time.Second}
		Expect(k8sClient.Update(ctx, &gs)).To(Succeed())

		res := reconcileWithResult(dir, key)
		Expect(statusOf(key).Phase).To(Equal(PhaseError))
		Expect(res.RequeueAfter).To(Equal(10*time.Second),
			"a failing GitSource must keep retrying, not go silent")
	})

	It("falls back to a default rather than never requeuing", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: zero-interval-cm\n  namespace: default\n")

		key := newGitSource("interval-zero", defaultNS)
		var gs gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
		gs.Spec.Interval = metav1.Duration{Duration: 0}
		Expect(k8sClient.Update(ctx, &gs)).To(Succeed())

		res := reconcileWithResult(dir, key)
		Expect(res.RequeueAfter).To(Equal(DefaultInterval),
			"interval 0 would stop drift correction entirely")
	})

	It("records lastSyncTime on success and leaves it alone on failure", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: synctime-cm\n  namespace: default\n")

		key := newGitSource("sync-time", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		good := statusOf(key)
		Expect(good.LastSyncTime).NotTo(BeNil())

		var gs gitopsv1alpha1.GitSource
		Expect(k8sClient.Get(ctx, key, &gs)).To(Succeed())
		gs.Spec.Path = "gone"
		Expect(k8sClient.Update(ctx, &gs)).To(Succeed())
		Expect(reconcileOnce(dir, key)).To(Succeed())

		bad := statusOf(key)
		Expect(bad.Phase).To(Equal(PhaseError))
		Expect(bad.LastSyncTime).To(Equal(good.LastSyncTime),
			"a failed attempt is not a sync, so the timestamp must not move")
	})

	It("clears driftedObjects once nothing is drifting", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: clears-cm\n  namespace: default\ndata:\n  k: v\n")

		key := newGitSource("clears-drift", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		cmKey := types.NamespacedName{Name: "clears-cm", Namespace: defaultNS}
		Expect(k8sClient.Get(ctx, cmKey, &cm)).To(Succeed())
		cm.Data["k"] = "tampered"
		Expect(k8sClient.Update(ctx, &cm, client.FieldOwner("a-human"))).To(Succeed())

		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(statusOf(key).DriftedObjects).To(HaveLen(1))
		Expect(statusOf(key).Phase).To(Equal(PhaseDrifted))

		By("reconciling again with nothing wrong")
		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(statusOf(key).DriftedObjects).To(BeEmpty(), "stale drift must not linger")
		Expect(statusOf(key).Phase).To(Equal(PhaseSynced))
	})

	It("names the changed field in the status message", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: message-cm\n  namespace: default\ndata:\n  colour: blue\n")

		key := newGitSource("drift-message", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		var cm corev1.ConfigMap
		cmKey := types.NamespacedName{Name: "message-cm", Namespace: defaultNS}
		Expect(k8sClient.Get(ctx, cmKey, &cm)).To(Succeed())
		cm.Data["colour"] = "red"
		Expect(k8sClient.Update(ctx, &cm, client.FieldOwner("a-human"))).To(Succeed())

		Expect(reconcileOnce(dir, key)).To(Succeed())
		Expect(statusOf(key).Message).To(ContainSubstring("data.colour"))
	})

	It("does not report a newly created object as drifted", func() {
		dir := GinkgoT().TempDir()
		writeManifest(dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fresh-cm\n  namespace: default\n")

		key := newGitSource("fresh-create", defaultNS)
		Expect(reconcileOnce(dir, key)).To(Succeed())

		Expect(statusOf(key).DriftedObjects).To(BeEmpty(), "a creation is not a correction")
		Expect(statusOf(key).Phase).To(Equal(PhaseSynced))
	})
})
