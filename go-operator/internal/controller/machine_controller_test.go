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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
	"github.com/homystack/nixos-operator/internal/ssh"
)

var _ = Describe("Machine Controller", func() {
	var testCounter int

	Context("When reconciling a resource with successful SSH", func() {
		var resourceName string
		var secretName string
		var typeNamespacedName types.NamespacedName

		ctx := context.Background()

		BeforeEach(func() {
			testCounter++
			resourceName = fmt.Sprintf("test-machine-%d", testCounter)
			secretName = fmt.Sprintf("test-ssh-key-%d", testCounter)
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			By("creating the SSH key secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: "default",
				},
				Type: corev1.SecretTypeSSHAuth,
				Data: map[string][]byte{
					"ssh-privatekey": []byte("fake-private-key"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("creating the custom resource for the Kind Machine")
			resource := &niov1alpha1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: niov1alpha1.MachineSpec{
					Host:    "test-host.example.com",
					SSHUser: "root",
					SSHKeySecretRef: &niov1alpha1.SecretReference{
						Name: secretName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			resource := &niov1alpha1.Machine{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				// Remove finalizer if present
				if len(resource.Finalizers) > 0 {
					resource.Finalizers = nil
					Expect(k8sClient.Update(ctx, resource)).To(Succeed())
				}
				By("Cleanup the specific resource instance Machine")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			secret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secret)
			if err == nil {
				By("Cleanup the SSH key secret")
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			}
		})

		It("should successfully reconcile the resource with mock SSH client", func() {
			By("Reconciling the created resource")
			mockSSH := &ssh.MockClient{
				CheckConnectionFunc: func(ctx context.Context, host string, port int, config *ssh.Config) error {
					return nil
				},
			}

			controllerReconciler := &MachineReconciler{
				Client:    k8sClient,
				Scheme:    k8sClient.Scheme(),
				Recorder:  record.NewFakeRecorder(10),
				SSHClient: mockSSH,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			machine := &niov1alpha1.Machine{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, machine)).To(Succeed())
			Expect(machine.Status.Discoverable).To(BeTrue())
		})
	})

	Context("When reconciling a resource with failed SSH", func() {
		var resourceName string
		var secretName string
		var typeNamespacedName types.NamespacedName

		ctx := context.Background()

		BeforeEach(func() {
			testCounter++
			resourceName = fmt.Sprintf("test-machine-%d", testCounter)
			secretName = fmt.Sprintf("test-ssh-key-%d", testCounter)
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			By("creating the SSH key secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: "default",
				},
				Type: corev1.SecretTypeSSHAuth,
				Data: map[string][]byte{
					"ssh-privatekey": []byte("fake-private-key"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("creating the custom resource for the Kind Machine")
			resource := &niov1alpha1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: niov1alpha1.MachineSpec{
					Host:    "test-host.example.com",
					SSHUser: "root",
					SSHKeySecretRef: &niov1alpha1.SecretReference{
						Name: secretName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			resource := &niov1alpha1.Machine{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				if len(resource.Finalizers) > 0 {
					resource.Finalizers = nil
					Expect(k8sClient.Update(ctx, resource)).To(Succeed())
				}
				By("Cleanup the specific resource instance Machine")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			secret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secret)
			if err == nil {
				By("Cleanup the SSH key secret")
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			}
		})

		It("should set Discoverable to false when SSH connection fails", func() {
			By("Reconciling the created resource with failing SSH")
			mockSSH := &ssh.MockClient{
				CheckConnectionFunc: func(ctx context.Context, host string, port int, config *ssh.Config) error {
					return context.DeadlineExceeded
				},
			}

			controllerReconciler := &MachineReconciler{
				Client:    k8sClient,
				Scheme:    k8sClient.Scheme(),
				Recorder:  record.NewFakeRecorder(10),
				SSHClient: mockSSH,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			machine := &niov1alpha1.Machine{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, machine)).To(Succeed())
			Expect(machine.Status.Discoverable).To(BeFalse())
		})
	})

	Context("When SSH secret is missing", func() {
		var resourceName string
		var typeNamespacedName types.NamespacedName

		ctx := context.Background()

		BeforeEach(func() {
			testCounter++
			resourceName = fmt.Sprintf("test-machine-%d", testCounter)
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			By("creating the custom resource for the Kind Machine without secret")
			resource := &niov1alpha1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: niov1alpha1.MachineSpec{
					Host:    "test-host.example.com",
					SSHUser: "root",
					SSHKeySecretRef: &niov1alpha1.SecretReference{
						Name: "non-existent-secret",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			resource := &niov1alpha1.Machine{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				if len(resource.Finalizers) > 0 {
					resource.Finalizers = nil
					Expect(k8sClient.Update(ctx, resource)).To(Succeed())
				}
				By("Cleanup the specific resource instance Machine")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should set CredentialsMissing condition when secret not found", func() {
			By("Reconciling the created resource without secret")
			mockSSH := &ssh.MockClient{}

			controllerReconciler := &MachineReconciler{
				Client:    k8sClient,
				Scheme:    k8sClient.Scheme(),
				Recorder:  record.NewFakeRecorder(10),
				SSHClient: mockSSH,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			machine := &niov1alpha1.Machine{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, machine)).To(Succeed())
			Expect(machine.Status.Discoverable).To(BeFalse())

			// Check that Discoverable condition has CredentialsMissing reason
			var discoverableCondition *metav1.Condition
			for i := range machine.Status.Conditions {
				if machine.Status.Conditions[i].Type == niov1alpha1.ConditionDiscoverable {
					discoverableCondition = &machine.Status.Conditions[i]
					break
				}
			}
			Expect(discoverableCondition).NotTo(BeNil())
			Expect(discoverableCondition.Reason).To(Equal(niov1alpha1.ReasonCredentialsMissing))
		})
	})
})

var _ = Describe("Machine resource not found", func() {
	It("should handle non-existent resource gracefully", func() {
		ctx := context.Background()
		mockSSH := &ssh.MockClient{}

		controllerReconciler := &MachineReconciler{
			Client:    k8sClient,
			Scheme:    k8sClient.Scheme(),
			Recorder:  record.NewFakeRecorder(10),
			SSHClient: mockSSH,
		}

		_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "non-existent",
				Namespace: "default",
			},
		})
		Expect(err).NotTo(HaveOccurred())
	})
})

// Suppress unused import error
var _ = errors.IsNotFound
