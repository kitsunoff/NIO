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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
)

var _ = Describe("NixosConfiguration Controller", func() {
	var configTestCounter int

	Context("When Machine is not found", func() {
		var resourceName string
		var typeNamespacedName types.NamespacedName

		ctx := context.Background()

		BeforeEach(func() {
			configTestCounter++
			resourceName = fmt.Sprintf("test-config-%d", configTestCounter)
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			By("creating the NixosConfiguration without existing Machine")
			resource := &niov1alpha1.NixosConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: niov1alpha1.NixosConfigurationSpec{
					MachineRef: niov1alpha1.MachineReference{
						Name: "non-existent-machine",
					},
					GitRepo: "https://github.com/example/nixos-config.git",
					Ref:     "main",
					Flake:   "#default",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			resource := &niov1alpha1.NixosConfiguration{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				if len(resource.Finalizers) > 0 {
					resource.Finalizers = nil
					Expect(k8sClient.Update(ctx, resource)).To(Succeed())
				}
				By("Cleanup the NixosConfiguration")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should set MachineNotReady condition when Machine not found", func() {
			By("Reconciling the created resource")
			controllerReconciler := &NixosConfigurationReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			config := &niov1alpha1.NixosConfiguration{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, config)).To(Succeed())

			// Check that Ready condition has MachineNotReady reason
			var readyCondition *metav1.Condition
			for i := range config.Status.Conditions {
				if config.Status.Conditions[i].Type == niov1alpha1.ConditionReady {
					readyCondition = &config.Status.Conditions[i]
					break
				}
			}
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Reason).To(Equal(niov1alpha1.ReasonMachineNotReady))
		})
	})

	Context("When Machine is not discoverable", func() {
		var resourceName string
		var machineName string
		var typeNamespacedName types.NamespacedName

		ctx := context.Background()

		BeforeEach(func() {
			configTestCounter++
			resourceName = fmt.Sprintf("test-config-%d", configTestCounter)
			machineName = fmt.Sprintf("test-machine-%d", configTestCounter)
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			By("creating a Machine that is not discoverable")
			machine := &niov1alpha1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      machineName,
					Namespace: "default",
				},
				Spec: niov1alpha1.MachineSpec{
					Host:    "unreachable-host.example.com",
					SSHUser: "root",
				},
				Status: niov1alpha1.MachineStatus{
					Discoverable: false,
				},
			}
			Expect(k8sClient.Create(ctx, machine)).To(Succeed())

			By("creating the NixosConfiguration")
			resource := &niov1alpha1.NixosConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: niov1alpha1.NixosConfigurationSpec{
					MachineRef: niov1alpha1.MachineReference{
						Name: machineName,
					},
					GitRepo: "https://github.com/example/nixos-config.git",
					Ref:     "main",
					Flake:   "#default",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			resource := &niov1alpha1.NixosConfiguration{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				if len(resource.Finalizers) > 0 {
					resource.Finalizers = nil
					Expect(k8sClient.Update(ctx, resource)).To(Succeed())
				}
				By("Cleanup the NixosConfiguration")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			machine := &niov1alpha1.Machine{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: machineName, Namespace: "default"}, machine)
			if err == nil {
				if len(machine.Finalizers) > 0 {
					machine.Finalizers = nil
					Expect(k8sClient.Update(ctx, machine)).To(Succeed())
				}
				By("Cleanup the Machine")
				Expect(k8sClient.Delete(ctx, machine)).To(Succeed())
			}
		})

		It("should set MachineNotReady condition when Machine is not discoverable", func() {
			By("Reconciling the created resource")
			controllerReconciler := &NixosConfigurationReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			config := &niov1alpha1.NixosConfiguration{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, config)).To(Succeed())

			// Check that Ready condition has MachineNotReady reason
			var readyCondition *metav1.Condition
			for i := range config.Status.Conditions {
				if config.Status.Conditions[i].Type == niov1alpha1.ConditionReady {
					readyCondition = &config.Status.Conditions[i]
					break
				}
			}
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Reason).To(Equal(niov1alpha1.ReasonMachineNotReady))
		})
	})
})

var _ = Describe("NixosConfiguration resource not found", func() {
	It("should handle non-existent resource gracefully", func() {
		ctx := context.Background()

		controllerReconciler := &NixosConfigurationReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(10),
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
