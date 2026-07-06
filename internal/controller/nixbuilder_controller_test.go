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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

var _ = Describe("NixBuilder Controller", func() {
	var counter int
	ctx := context.Background()

	newReconciler := func() *NixBuilderReconciler {
		return &NixBuilderReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
	}

	Context("When reconciling a NixBuilder without storage", func() {
		var name string
		var nn types.NamespacedName

		BeforeEach(func() {
			counter++
			name = fmt.Sprintf("builder-%d", counter)
			nn = types.NamespacedName{Name: name, Namespace: "default"}
			maxJobs := int32(2)
			builder := &niov1alpha1.NixBuilder{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: niov1alpha1.NixBuilderSpec{
					StoreRef: &niov1alpha1.LocalObjectReference{Name: "store"},
					MaxJobs:  &maxJobs,
				},
			}
			Expect(k8sClient.Create(ctx, builder)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixBuilder{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}})
		})

		It("creates a single-worker StatefulSet with an emptyDir /nix and publishes the endpoint", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, nn, &sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(Equal(int32(1)))
			Expect(sts.Spec.VolumeClaimTemplates).To(BeEmpty())
			hasEmptyDir := false
			for _, v := range sts.Spec.Template.Spec.Volumes {
				if v.Name == builderStoreVolumeName && v.EmptyDir != nil {
					hasEmptyDir = true
				}
			}
			Expect(hasEmptyDir).To(BeTrue())

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, nn, &svc)).To(Succeed())
			Expect(svc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))

			var builder niov1alpha1.NixBuilder
			Expect(k8sClient.Get(ctx, nn, &builder)).To(Succeed())
			Expect(builder.Status.BuilderEndpoint).To(Equal(fmt.Sprintf("ssh-ng://root@%s.default.svc", name)))
			Expect(builder.Status.Ready).To(BeFalse())
			Expect(builder.Status.Phase).To(Equal("Pending"))
		})
	})

	Context("When reconciling a NixBuilder with persistent storage", func() {
		var name string
		var nn types.NamespacedName

		BeforeEach(func() {
			counter++
			name = fmt.Sprintf("builder-pvc-%d", counter)
			nn = types.NamespacedName{Name: name, Namespace: "default"}
			builder := &niov1alpha1.NixBuilder{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: niov1alpha1.NixBuilderSpec{
					Storage: &corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, builder)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixBuilder{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}})
		})

		It("uses a volumeClaimTemplate for /nix", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, nn, &sts)).To(Succeed())
			Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1))
			Expect(sts.Spec.VolumeClaimTemplates[0].Name).To(Equal(builderStoreVolumeName))
		})
	})
})
