/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

var _ = Describe("NixDeployment Controller", func() {
	var counter int
	ctx := context.Background()

	newReconciler := func() *NixDeploymentReconciler {
		return &NixDeploymentReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(30),
			Git:      fakeGit{sha: "should-not-be-used"},
		}
	}

	// createReadyStore creates a NixStore and forces its status to Ready.
	createReadyStore := func(name string) {
		store := &niov1alpha1.NixStore{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: niov1alpha1.NixStoreSpec{
				Storage: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				},
			},
		}
		Expect(k8sClient.Create(ctx, store)).To(Succeed())
		store.Status.Phase = niov1alpha1.PhaseReady
		store.Status.SubstituterURL = fmt.Sprintf("http://%s.default.svc:5000", name)
		store.Status.PublicKey = name + "-1:AAAA"
		Expect(k8sClient.Status().Update(ctx, store)).To(Succeed())
	}

	Context("When reconciling a NixDeployment with a ready store", func() {
		var name, storeName string
		var nn types.NamespacedName

		BeforeEach(func() {
			counter++
			name = fmt.Sprintf("nd-%d", counter)
			storeName = fmt.Sprintf("nd-store-%d", counter)
			nn = types.NamespacedName{Name: name, Namespace: "default"}
			createReadyStore(storeName)

			replicas := int32(2)
			nd := &niov1alpha1.NixDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: niov1alpha1.NixDeploymentSpec{
					Nix: niov1alpha1.NixSpec{
						Source:   niov1alpha1.NixSource{GitRepo: "https://example.com/r", Rev: "abcdef1234567890"},
						Run:      ".#server",
						Args:     []string{"--port", "8080"},
						StoreRef: &niov1alpha1.LocalObjectReference{Name: storeName},
					},
					DeploymentTemplate: &appsv1.DeploymentSpec{Replicas: &replicas},
				},
			}
			Expect(k8sClient.Create(ctx, nd)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixDeployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}})
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixStore{ObjectMeta: metav1.ObjectMeta{Name: storeName, Namespace: "default"}})
		})

		It("projects a Deployment with the rendered pod template and publishes status", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, nn, &dep)).To(Succeed())

			By("stamping three init-containers and the app command")
			Expect(dep.Spec.Template.Spec.InitContainers).To(HaveLen(3))
			var app *corev1.Container
			for i := range dep.Spec.Template.Spec.Containers {
				if dep.Spec.Template.Spec.Containers[i].Name == "app" {
					app = &dep.Spec.Template.Spec.Containers[i]
				}
			}
			Expect(app).NotTo(BeNil())
			Expect(app.Command).To(Equal([]string{"nix", "run", ".#server", "--", "--port", "8080"}))

			By("defaulting a surge-only strategy")
			Expect(dep.Spec.Strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
			Expect(dep.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue()).To(Equal(0))

			By("stamping a managed selector matching the pod labels")
			Expect(dep.Spec.Selector.MatchLabels).To(HaveKeyWithValue(niov1alpha1.LabelWorkloadName, name))
			Expect(dep.Spec.Template.Labels).To(HaveKey(niov1alpha1.LabelRevision))

			By("publishing status: resolved revision, WorkloadRef, GitSynced, finalizer")
			var got niov1alpha1.NixDeployment
			Expect(k8sClient.Get(ctx, nn, &got)).To(Succeed())
			Expect(got.Status.ResolvedRevision).To(Equal("abcdef1234567890"))
			Expect(got.Status.WorkloadRef).To(Equal(name))
			Expect(got.Finalizers).To(ContainElement(niov1alpha1.WorkloadFinalizer))
			Expect(got.Status.Phase).To(Or(Equal(niov1alpha1.PhaseProgressing), Equal(niov1alpha1.PhaseBuilding)))
		})

		It("marks the rollout Stalled when new-revision pods fail their init build", func() {
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			rev := compositeRevision("abcdef1234567890", ".#server", []string{"--port", "8080"})
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-broken",
					Namespace: "default",
					Labels: map[string]string{
						niov1alpha1.LabelWorkloadKind: kindNixDeployment,
						niov1alpha1.LabelWorkloadName: name,
						niov1alpha1.LabelRevision:     rev,
					},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
				Name:  initInstantiate,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}},
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var got niov1alpha1.NixDeployment
			Expect(k8sClient.Get(ctx, nn, &got)).To(Succeed())
			Expect(got.Status.Phase).To(Equal(niov1alpha1.PhaseDegraded))
			cond := findCondition(got.Status.Conditions, niov1alpha1.ConditionStalled)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))

			_ = k8sClient.Delete(ctx, pod)
		})
	})

	Context("When the referenced store is not ready", func() {
		var name string
		var nn types.NamespacedName

		BeforeEach(func() {
			counter++
			name = fmt.Sprintf("nd-noinfra-%d", counter)
			nn = types.NamespacedName{Name: name, Namespace: "default"}
			nd := &niov1alpha1.NixDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: niov1alpha1.NixDeploymentSpec{
					Nix: niov1alpha1.NixSpec{
						Source:   niov1alpha1.NixSource{Rev: "abcdef1"},
						Run:      ".",
						StoreRef: &niov1alpha1.LocalObjectReference{Name: "missing-store"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nd)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixDeployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}})
		})

		It("stalls without creating the Deployment", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			err = k8sClient.Get(ctx, nn, &dep)
			Expect(err).To(HaveOccurred(), "no Deployment should be created while infra is not ready")

			var got niov1alpha1.NixDeployment
			Expect(k8sClient.Get(ctx, nn, &got)).To(Succeed())
			Expect(got.Status.Phase).To(Equal(niov1alpha1.PhaseDegraded))
			cond := findCondition(got.Status.Conditions, niov1alpha1.ConditionStalled)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal(reasonInfraNotReady))
		})
	})

	Context("Suspend and deletion", func() {
		It("sets Suspended and does not project", func() {
			counter++
			name := fmt.Sprintf("nd-susp-%d", counter)
			nn := types.NamespacedName{Name: name, Namespace: "default"}
			nd := &niov1alpha1.NixDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: niov1alpha1.NixDeploymentSpec{
					Nix: niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{Rev: "abcdef1"}, Run: ".", Suspend: true},
				},
			}
			Expect(k8sClient.Create(ctx, nd)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, nd) })

			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var got niov1alpha1.NixDeployment
			Expect(k8sClient.Get(ctx, nn, &got)).To(Succeed())
			Expect(got.Status.Phase).To(Equal(niov1alpha1.PhaseSuspended))
		})
	})
})

// findCondition returns a pointer to the named condition, or nil.
func findCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}
