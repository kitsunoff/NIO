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
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

// cronObserveFixture builds a NixCronJob, its projected batch CronJob, and a
// reconciler over a fake client holding both plus any extra objects.
func cronObserveFixture(t *testing.T, extra ...client.Object) (
	*NixCronJobReconciler, *niov1alpha1.NixCronJob,
) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(nio): %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(batch): %v", err)
	}

	ncj := &niov1alpha1.NixCronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nightly", Namespace: "apps", UID: types.UID("ncj-uid"),
		},
		Spec: niov1alpha1.NixCronJobSpec{
			Nix: niov1alpha1.NixSpec{
				Source: niov1alpha1.NixSource{GitRepo: "https://example.com/r", Ref: "main"},
				Run:    ".#report",
			},
		},
	}
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nightly", Namespace: "apps", UID: types.UID("cj-uid"),
		},
	}

	objects := append([]client.Object{ncj, cj}, extra...)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &NixCronJobReconciler{Client: c, Scheme: scheme}, ncj
}

// failedJob returns a Job owned by owner that finished with JobFailed.
func failedJob(name string, owner types.UID, at metav1.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "apps",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "CronJob", Name: "nightly", UID: owner,
			}},
		},
		Status: batchv1.JobStatus{
			CompletionTime: &at,
			Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
				LastTransitionTime: at, Reason: "BackoffLimitExceeded",
			}},
		},
	}
}

// A CronJob keeps scheduling whether or not its runs succeed, so observing only
// "the CronJob exists" reported Ready forever. A failed run must make the
// workload Degraded — this is what lets a NixCluster report a broken converge.
func TestCronObserve_FailedRunIsDegraded(t *testing.T) {
	failedAt := metav1.NewTime(time.Now().Add(-time.Minute).UTC())
	r, ncj := cronObserveFixture(t, failedJob("nightly-1", types.UID("cj-uid"), failedAt))

	r.observe(context.Background(), ncj, resolvedSource{revision: "abc1234"})

	if ncj.Status.Phase != niov1alpha1.PhaseDegraded {
		t.Errorf("phase = %q, want %q", ncj.Status.Phase, niov1alpha1.PhaseDegraded)
	}
	if ncj.Status.LastFailedTime == nil {
		t.Fatal("LastFailedTime must record the failed run")
	}
	ready := meta.FindStatusCondition(ncj.Status.Conditions, niov1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != reasonRunFailed {
		t.Errorf("Ready condition = %+v, want False/%s", ready, reasonRunFailed)
	}
}

// An immediate Job fired on a revision change is owned by the NixCronJob itself,
// not by the projected CronJob — its failures must count too.
func TestCronObserve_FailedImmediateJobCounts(t *testing.T) {
	failedAt := metav1.NewTime(time.Now().Add(-time.Minute).UTC())
	r, ncj := cronObserveFixture(t, failedJob("nightly-now", types.UID("ncj-uid"), failedAt))

	r.observe(context.Background(), ncj, resolvedSource{revision: "abc1234"})

	if ncj.Status.Phase != niov1alpha1.PhaseDegraded {
		t.Errorf("phase = %q, want %q", ncj.Status.Phase, niov1alpha1.PhaseDegraded)
	}
}

// A Job belonging to somebody else must never affect this workload's phase.
func TestCronObserve_ForeignFailedJobIgnored(t *testing.T) {
	failedAt := metav1.NewTime(time.Now().Add(-time.Minute).UTC())
	r, ncj := cronObserveFixture(t, failedJob("someone-else", types.UID("other-uid"), failedAt))

	r.observe(context.Background(), ncj, resolvedSource{revision: "abc1234"})

	if ncj.Status.Phase != niov1alpha1.PhaseReady {
		t.Errorf("phase = %q, want %q (a foreign Job must not degrade us)",
			ncj.Status.Phase, niov1alpha1.PhaseReady)
	}
	if ncj.Status.LastFailedTime != nil {
		t.Errorf("LastFailedTime = %v, want nil", ncj.Status.LastFailedTime)
	}
}

// A cron whose runs all succeeded stays Ready.
func TestCronObserve_NoFailuresIsReady(t *testing.T) {
	r, ncj := cronObserveFixture(t)

	r.observe(context.Background(), ncj, resolvedSource{revision: "abc1234"})

	if ncj.Status.Phase != niov1alpha1.PhaseReady {
		t.Errorf("phase = %q, want %q", ncj.Status.Phase, niov1alpha1.PhaseReady)
	}
	if ncj.Status.RolledOutRevision != "abc1234" {
		t.Errorf("RolledOutRevision = %q, want abc1234", ncj.Status.RolledOutRevision)
	}
}
