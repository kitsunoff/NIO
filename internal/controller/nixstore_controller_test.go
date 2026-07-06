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
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

var _ = Describe("NixStore Controller", func() {
	var counter int

	newReconciler := func() *NixStoreReconciler {
		return &NixStoreReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
	}

	storageSpec := func() corev1.PersistentVolumeClaimSpec {
		return corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		}
	}

	Context("When reconciling a NixStore without a signing-key ref", func() {
		var name string
		var nn types.NamespacedName
		ctx := context.Background()

		BeforeEach(func() {
			counter++
			name = fmt.Sprintf("store-%d", counter)
			nn = types.NamespacedName{Name: name, Namespace: "default"}
			store := &niov1alpha1.NixStore{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec:       niov1alpha1.NixStoreSpec{Storage: storageSpec()},
			}
			Expect(k8sClient.Create(ctx, store)).To(Succeed())
		})

		AfterEach(func() {
			store := &niov1alpha1.NixStore{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
			_ = k8sClient.Delete(ctx, store)
		})

		It("generates a valid Nix signing key, creates the Service and StatefulSet, and publishes status", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("generating an owned signing-key Secret with a valid ed25519 public key")
			var secret corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-signing-key", Namespace: "default"}, &secret)).To(Succeed())
			pub := string(secret.Data[SigningKeySecretPublicField])
			priv := string(secret.Data[SigningKeySecretPrivateField])
			Expect(pub).To(ContainSubstring(":"))
			Expect(priv).To(ContainSubstring(":"))
			// Public key part must decode to a 32-byte ed25519 public key.
			pubB64 := pub[strings.Index(pub, ":")+1:]
			raw, decErr := base64.StdEncoding.DecodeString(pubB64)
			Expect(decErr).NotTo(HaveOccurred())
			Expect(raw).To(HaveLen(ed25519.PublicKeySize))
			Expect(secret.OwnerReferences).NotTo(BeEmpty())

			By("creating a headless Service")
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, nn, &svc)).To(Succeed())
			Expect(svc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))
			Expect(svc.Spec.Selector).To(HaveKeyWithValue(niov1alpha1.LabelWorkloadName, name))

			By("creating a StatefulSet with the /nix volumeClaimTemplate and signing-key mount")
			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, nn, &sts)).To(Succeed())
			Expect(sts.Spec.ServiceName).To(Equal(name))
			Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1))
			Expect(sts.Spec.VolumeClaimTemplates[0].Name).To(Equal(nixStoreVolumeName))
			var storeContainer *corev1.Container
			for i := range sts.Spec.Template.Spec.Containers {
				if sts.Spec.Template.Spec.Containers[i].Name == "store" {
					storeContainer = &sts.Spec.Template.Spec.Containers[i]
				}
			}
			Expect(storeContainer).NotTo(BeNil())
			mountPaths := map[string]bool{}
			for _, m := range storeContainer.VolumeMounts {
				mountPaths[m.MountPath] = true
			}
			Expect(mountPaths).To(HaveKey("/nix"))
			Expect(mountPaths).To(HaveKey("/etc/nix/signing"))

			By("publishing substituter/store endpoints and public key, Pending until ready")
			var store niov1alpha1.NixStore
			Expect(k8sClient.Get(ctx, nn, &store)).To(Succeed())
			Expect(store.Status.SubstituterURL).To(Equal(fmt.Sprintf("http://%s.default.svc:%d", name, NixStoreHTTPPort)))
			Expect(store.Status.StoreURI).To(ContainSubstring("ssh-ng://"))
			Expect(store.Status.PublicKey).To(Equal(pub))
			Expect(store.Status.Phase).To(Equal("Pending"))
		})

		It("is idempotent across repeated reconciles and does not regenerate the key", func() {
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			var first corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-signing-key", Namespace: "default"}, &first)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			var second corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-signing-key", Namespace: "default"}, &second)).To(Succeed())
			Expect(second.Data[SigningKeySecretPrivateField]).To(Equal(first.Data[SigningKeySecretPrivateField]))
		})
	})

	Context("When a signing-key Secret is provided", func() {
		var name, secretName string
		var nn types.NamespacedName
		ctx := context.Background()

		BeforeEach(func() {
			counter++
			name = fmt.Sprintf("store-ref-%d", counter)
			secretName = fmt.Sprintf("user-key-%d", counter)
			nn = types.NamespacedName{Name: name, Namespace: "default"}

			userSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
				Data: map[string][]byte{
					SigningKeySecretPublicField:  []byte("mykey-1:AAAABBBBCCCCDDDD"),
					SigningKeySecretPrivateField: []byte("mykey-1:secret"),
				},
			}
			Expect(k8sClient.Create(ctx, userSecret)).To(Succeed())

			store := &niov1alpha1.NixStore{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: niov1alpha1.NixStoreSpec{
					Storage:             storageSpec(),
					SigningKeySecretRef: &niov1alpha1.SecretReference{Name: secretName},
				},
			}
			Expect(k8sClient.Create(ctx, store)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixStore{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}})
			_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"}})
		})

		It("uses the provided public key and does not generate one", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var store niov1alpha1.NixStore
			Expect(k8sClient.Get(ctx, nn, &store)).To(Succeed())
			Expect(store.Status.PublicKey).To(Equal("mykey-1:AAAABBBBCCCCDDDD"))

			generated := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: name + "-signing-key", Namespace: "default"}, generated)
			Expect(client.IgnoreNotFound(err)).To(Succeed())
			Expect(err).To(HaveOccurred(), "no generated secret should exist when a ref is provided")
		})
	})
})
