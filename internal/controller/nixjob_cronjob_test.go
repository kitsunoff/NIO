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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

func readyStore(ctx context.Context, name string) {
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

var _ = Describe("NixJob Controller", func() {
	var counter int
	ctx := context.Background()

	newReconciler := func() *NixJobReconciler {
		return &NixJobReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(30), Git: fakeGit{sha: "unused"}}
	}

	It("creates an immutable per-revision run-Job with a batch restart policy", func() {
		counter++
		name := fmt.Sprintf("nj-%d", counter)
		storeName := fmt.Sprintf("nj-store-%d", counter)
		nn := types.NamespacedName{Name: name, Namespace: "default"}
		readyStore(ctx, storeName)
		nj := &niov1alpha1.NixJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: niov1alpha1.NixJobSpec{
				Nix: niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{Rev: "abcdef1234567890"}, Run: ".#build", StoreRef: &niov1alpha1.LocalObjectReference{Name: storeName}},
			},
		}
		Expect(k8sClient.Create(ctx, nj)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, nj)
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixStore{ObjectMeta: metav1.ObjectMeta{Name: storeName, Namespace: "default"}})
		})

		r := newReconciler()
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())

		rev := compositeRevision("abcdef1234567890", ".#build", nil)
		jobName := name + "-" + rev
		var job batchv1.Job
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: "default"}, &job)).To(Succeed())
		Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(3))
		Expect(job.Spec.Template.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))

		var got niov1alpha1.NixJob
		Expect(k8sClient.Get(ctx, nn, &got)).To(Succeed())
		Expect(got.Status.ActiveJob).To(Equal(jobName))

		// Idempotent: a second reconcile does not create a second Job.
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())
		var jobs batchv1.JobList
		Expect(k8sClient.List(ctx, &jobs, client.InNamespace("default"), client.MatchingLabels{
			niov1alpha1.LabelWorkloadName: name, niov1alpha1.LabelWorkloadKind: kindNixJob,
		})).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
	})

	It("reports Failed when the run-Job exhausts its backoffLimit", func() {
		counter++
		name := fmt.Sprintf("njf-%d", counter)
		storeName := fmt.Sprintf("njf-store-%d", counter)
		nn := types.NamespacedName{Name: name, Namespace: "default"}
		readyStore(ctx, storeName)
		nj := &niov1alpha1.NixJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       niov1alpha1.NixJobSpec{Nix: niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{Rev: "abcdef1"}, Run: ".", StoreRef: &niov1alpha1.LocalObjectReference{Name: storeName}}},
		}
		Expect(k8sClient.Create(ctx, nj)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, nj)
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixStore{ObjectMeta: metav1.ObjectMeta{Name: storeName, Namespace: "default"}})
		})

		r := newReconciler()
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())

		rev := compositeRevision("abcdef1", ".", nil)
		var job batchv1.Job
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-" + rev, Namespace: "default"}, &job)).To(Succeed())
		now := metav1.Now()
		job.Status.StartTime = &now
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
		}
		Expect(k8sClient.Status().Update(ctx, &job)).To(Succeed())

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())
		var got niov1alpha1.NixJob
		Expect(k8sClient.Get(ctx, nn, &got)).To(Succeed())
		Expect(got.Status.Phase).To(Equal(niov1alpha1.PhaseFailed))
	})
})

var _ = Describe("NixCronJob Controller", func() {
	var counter int
	ctx := context.Background()

	newReconciler := func() *NixCronJobReconciler {
		return &NixCronJobReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(30), Git: fakeGit{sha: "unused"}}
	}

	It("projects a CronJob with the rendered jobTemplate pinned to the revision", func() {
		counter++
		name := fmt.Sprintf("ncj-%d", counter)
		storeName := fmt.Sprintf("ncj-store-%d", counter)
		nn := types.NamespacedName{Name: name, Namespace: "default"}
		readyStore(ctx, storeName)
		ncj := &niov1alpha1.NixCronJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: niov1alpha1.NixCronJobSpec{
				Nix:             niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{Rev: "abcdef1"}, Run: ".#task", StoreRef: &niov1alpha1.LocalObjectReference{Name: storeName}},
				CronJobTemplate: batchv1.CronJobSpec{Schedule: "*/5 * * * *"},
			},
		}
		Expect(k8sClient.Create(ctx, ncj)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, ncj)
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixStore{ObjectMeta: metav1.ObjectMeta{Name: storeName, Namespace: "default"}})
		})

		_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())

		var cj batchv1.CronJob
		Expect(k8sClient.Get(ctx, nn, &cj)).To(Succeed())
		Expect(cj.Spec.Schedule).To(Equal("*/5 * * * *"))
		Expect(cj.Spec.JobTemplate.Spec.Template.Spec.InitContainers).To(HaveLen(3))
		Expect(cj.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))

		var got niov1alpha1.NixCronJob
		Expect(k8sClient.Get(ctx, nn, &got)).To(Succeed())
		Expect(got.Status.Phase).To(Equal(niov1alpha1.PhaseReady))
	})

	It("fires a one-off Job on a revision change when triggerOnChange is true", func() {
		counter++
		name := fmt.Sprintf("ncjt-%d", counter)
		storeName := fmt.Sprintf("ncjt-store-%d", counter)
		nn := types.NamespacedName{Name: name, Namespace: "default"}
		readyStore(ctx, storeName)
		trigger := true
		ncj := &niov1alpha1.NixCronJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: niov1alpha1.NixCronJobSpec{
				Nix:             niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{Rev: "abcdef1"}, Run: ".", TriggerOnChange: &trigger, StoreRef: &niov1alpha1.LocalObjectReference{Name: storeName}},
				CronJobTemplate: batchv1.CronJobSpec{Schedule: "*/5 * * * *"},
			},
		}
		Expect(k8sClient.Create(ctx, ncj)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, ncj)
			_ = k8sClient.Delete(ctx, &niov1alpha1.NixStore{ObjectMeta: metav1.ObjectMeta{Name: storeName, Namespace: "default"}})
		})

		_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())

		rev := compositeRevision("abcdef1", ".", nil)
		var job batchv1.Job
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-" + rev + "-manual", Namespace: "default"}, &job)).To(Succeed())
		Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(3))
	})
})
