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

var _ = Describe("NixStatefulSet Controller", func() {
	var counter int
	ctx := context.Background()

	newReconciler := func() *NixStatefulSetReconciler {
		return &NixStatefulSetReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(30),
			Git:      fakeGit{sha: "unused"},
		}
	}

	makeReadyStore := func(name string) {
		store := &niov1alpha1.NixStore{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       niov1alpha1.NixStoreSpec{Storage: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}}},
		}
		Expect(k8sClient.Create(ctx, store)).To(Succeed())
		store.Status.Phase = niov1alpha1.PhaseReady
		store.Status.SubstituterURL = "http://" + name + ".default.svc:5000"
		store.Status.PublicKey = name + "-1:AAAA"
		Expect(k8sClient.Status().Update(ctx, store)).To(Succeed())
	}

	Context("When reconciling a NixStatefulSet with a ready store", func() {
		var name, storeName string
		var nn types.NamespacedName

		BeforeEach(func() {
			counter++
			name = fmt.Sprintf("nss-%d", counter)
			storeName = fmt.Sprintf("nss-store-%d", counter)
			nn = types.NamespacedName{Name: name, Namespace: "default"}
			makeReadyStore(storeName)

			replicas := int32(3)
			nss := &niov1alpha1.NixStatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: niov1alpha1.NixStatefulSetSpec{
					Nix: niov1alpha1.NixSpec{
						Source:   niov1alpha1.NixSource{Rev: "abcdef1234567890"},
						Run:      ".#server",
						StoreRef: &niov1alpha1.LocalObjectReference{Name: storeName},
					},
					StatefulSetTemplate: appsv1.StatefulSetSpec{
						ServiceName: name + "-svc",
						Replicas:    &replicas,
					},
				},
			}
			Expect(k8sClient.Create(ctx, nss)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixStatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}})
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixStore{ObjectMeta: metav1.ObjectMeta{Name: storeName, Namespace: "default"}})
		})

		It("projects a StatefulSet preserving serviceName, with the rendered pod template and no maxUnavailable default", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, nn, &sts)).To(Succeed())
			Expect(sts.Spec.ServiceName).To(Equal(name + "-svc"))
			Expect(sts.Spec.Template.Spec.InitContainers).To(HaveLen(3))
			Expect(sts.Spec.Selector.MatchLabels).To(HaveKeyWithValue(niov1alpha1.LabelWorkloadName, name))
			// StatefulSet gets no operator-set maxUnavailable (ordered update).
			if sts.Spec.UpdateStrategy.RollingUpdate != nil {
				Expect(sts.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable).To(BeNil())
			}

			var got niov1alpha1.NixStatefulSet
			Expect(k8sClient.Get(ctx, nn, &got)).To(Succeed())
			Expect(got.Status.ResolvedRevision).To(Equal("abcdef1234567890"))
			Expect(got.Status.WorkloadRef).To(Equal(name))
			Expect(got.Finalizers).To(ContainElement(niov1alpha1.WorkloadFinalizer))
		})

		It("stalls when a new-revision pod fails its init build", func() {
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			rev := compositeRevision("abcdef1234567890", ".#server", nil)
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name + "-0",
					Namespace: "default",
					Labels: map[string]string{
						niov1alpha1.LabelWorkloadKind: kindNixStatefulSet,
						niov1alpha1.LabelWorkloadName: name,
						niov1alpha1.LabelRevision:     rev,
					},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
				Name:  initInstantiate,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2, Reason: "Error"}},
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var got niov1alpha1.NixStatefulSet
			Expect(k8sClient.Get(ctx, nn, &got)).To(Succeed())
			Expect(got.Status.Phase).To(Equal(niov1alpha1.PhaseDegraded))
			Expect(findCondition(got.Status.Conditions, niov1alpha1.ConditionStalled)).NotTo(BeNil())

			_ = k8sClient.Delete(ctx, pod)
		})
	})
})
