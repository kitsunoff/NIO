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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const (
	smName = "web"
	smRev  = "cafef00d"
)

// smConfig builds a NixosConfiguration for the state-machine tests.
func smConfig() *niov1alpha1.NixosConfiguration {
	return &niov1alpha1.NixosConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: niov1alpha1.NixosConfigurationSpec{
			MachineRef: niov1alpha1.MachineReference{Name: "node-01"},
			GitRepo:    "https://github.com/example/nixos.git",
			Ref:        "main",
			Flake:      "#web",
		},
	}
}

// smMachine builds a discoverable Machine with an SSH key.
func smMachine() *niov1alpha1.Machine {
	return &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec: niov1alpha1.MachineSpec{
			Host:            "10.0.0.5",
			SSHUser:         "root",
			SSHKeySecretRef: &niov1alpha1.SecretReference{Name: "node-01-ssh"},
		},
		Status: niov1alpha1.MachineStatus{Discoverable: true},
	}
}

func smScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func smReconciler(t *testing.T, objs ...client.Object) (*NixosConfigurationReconciler, client.Client) {
	t.Helper()
	s := smScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(
			&niov1alpha1.NixosConfiguration{},
			&niov1alpha1.Machine{},
			&niov1alpha1.NixJob{},
			&niov1alpha1.NixCronJob{},
		).
		Build()
	r := &NixosConfigurationReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(50)}
	return r, c
}

func smReconcile(t *testing.T, r *NixosConfigurationReconciler, name string) {
	t.Helper()
	smReconcileResult(t, r, name)
}

// smReconcileResult reconciles once and returns the ctrl.Result so tests can
// assert on requeue behaviour.
func smReconcileResult(t *testing.T, r *NixosConfigurationReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func getConfig(t *testing.T, c client.Client, name string) *niov1alpha1.NixosConfiguration {
	t.Helper()
	var cfg niov1alpha1.NixosConfiguration
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &cfg); err != nil {
		t.Fatalf("get config: %v", err)
	}
	return &cfg
}

// TestReconcile_NonInstall_ConvergingThenReady drives the non-install path:
// Pending -> Converging (day-2 cron created) -> Ready (cron healthy) with
// Machine writeback.
func TestReconcile_NonInstall_ConvergingThenReady(t *testing.T) {
	r, c := smReconciler(t, smConfig(), smMachine())

	// First reconcile: finalizer added, day-2 cron created, Converging.
	smReconcile(t, r, "web")

	cfg := getConfig(t, c, "web")
	if !containsFinalizer(cfg.Finalizers, niov1alpha1.FinalizerName) {
		t.Errorf("finalizer not added: %v", cfg.Finalizers)
	}
	if cfg.Status.Phase != niov1alpha1.NixosConfigPhaseConverging {
		t.Errorf("phase = %q, want Converging", cfg.Status.Phase)
	}

	var cron niov1alpha1.NixCronJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-day2", Namespace: "default"}, &cron); err != nil {
		t.Fatalf("day-2 cron not created: %v", err)
	}
	if len(cron.OwnerReferences) != 1 || cron.OwnerReferences[0].Name != smName {
		t.Errorf("day-2 cron ownerRef = %v, want controller ref to web", cron.OwnerReferences)
	}
	if cfg.Status.DayTwoCronJobRef != "web-day2" {
		t.Errorf("DayTwoCronJobRef = %q", cfg.Status.DayTwoCronJobRef)
	}

	// Simulate the day-2 cron becoming healthy for a resolved revision.
	now := metav1.Now()
	cron.Status.Phase = niov1alpha1.PhaseReady
	cron.Status.RolledOutRevision = smRev
	cron.Status.LastSuccessfulTime = &now
	if err := c.Status().Update(context.Background(), &cron); err != nil {
		t.Fatalf("seed cron status: %v", err)
	}

	// Second reconcile: Ready + machine writeback.
	smReconcile(t, r, "web")

	cfg = getConfig(t, c, "web")
	if cfg.Status.Phase != niov1alpha1.NixosConfigPhaseReady {
		t.Errorf("phase = %q, want Ready", cfg.Status.Phase)
	}
	if cfg.Status.ResolvedRevision != smRev {
		t.Errorf("ResolvedRevision = %q, want cafef00d", cfg.Status.ResolvedRevision)
	}

	var machine niov1alpha1.Machine
	if err := c.Get(context.Background(), types.NamespacedName{Name: "node-01", Namespace: "default"}, &machine); err != nil {
		t.Fatalf("get machine: %v", err)
	}
	if !machine.Status.HasConfiguration {
		t.Error("machine writeback: HasConfiguration not set")
	}
	if machine.Status.AppliedConfiguration != smName {
		t.Errorf("machine AppliedConfiguration = %q, want web", machine.Status.AppliedConfiguration)
	}
	if machine.Status.AppliedCommit != smRev {
		t.Errorf("machine AppliedCommit = %q, want resolvedRevision cafef00d", machine.Status.AppliedCommit)
	}
}

// TestReconcile_FullInstall_Path drives the install path:
// Installing (install NixJob created, ownerRef) -> on success the install child
// is deleted, FullDiskInstallCompleted is set, and the day-2 cron is created.
func TestReconcile_FullInstall_Path(t *testing.T) {
	cfg := smConfig()
	cfg.Spec.FullInstall = true
	r, c := smReconciler(t, cfg, smMachine())

	smReconcile(t, r, "web")

	got := getConfig(t, c, "web")
	if got.Status.Phase != niov1alpha1.NixosConfigPhaseInstalling {
		t.Errorf("phase = %q, want Installing", got.Status.Phase)
	}
	var install niov1alpha1.NixJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-install", Namespace: "default"}, &install); err != nil {
		t.Fatalf("install NixJob not created: %v", err)
	}
	if len(install.OwnerReferences) != 1 || install.OwnerReferences[0].Name != smName {
		t.Errorf("install ownerRef = %v, want controller ref to web", install.OwnerReferences)
	}

	// Simulate the install completing.
	install.Status.Succeeded = 1
	if err := c.Status().Update(context.Background(), &install); err != nil {
		t.Fatalf("seed install status: %v", err)
	}

	smReconcile(t, r, "web")

	got = getConfig(t, c, "web")
	if !got.Status.FullDiskInstallCompleted {
		t.Error("FullDiskInstallCompleted not set after install success")
	}
	// Install child deleted.
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-install", Namespace: "default"}, &install); err == nil {
		t.Error("install NixJob should have been deleted after success")
	}
	// Day-2 cron created; phase Converging.
	var cron niov1alpha1.NixCronJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-day2", Namespace: "default"}, &cron); err != nil {
		t.Fatalf("day-2 cron not created after install: %v", err)
	}
	if got.Status.Phase != niov1alpha1.NixosConfigPhaseConverging {
		t.Errorf("phase = %q, want Converging after install", got.Status.Phase)
	}
}

// TestReconcile_FullInstall_FailureBoundedByRetries checks a failing install is
// retried up to the cap, then held in Degraded.
func TestReconcile_FullInstall_FailureBoundedByRetries(t *testing.T) {
	cfg := smConfig()
	cfg.Spec.FullInstall = true
	r, c := smReconciler(t, cfg, smMachine())

	for attempt := 1; attempt <= MaxInstallRetries+1; attempt++ {
		smReconcile(t, r, "web")
		var install niov1alpha1.NixJob
		err := c.Get(context.Background(), types.NamespacedName{Name: "web-install", Namespace: "default"}, &install)
		if err != nil {
			// Recreated on the next reconcile; skip seeding this round.
			continue
		}
		install.Status.Failed = 1
		if err := c.Status().Update(context.Background(), &install); err != nil {
			t.Fatalf("seed install failure: %v", err)
		}
		// Trigger the failure handling.
		smReconcile(t, r, "web")
	}

	got := getConfig(t, c, "web")
	if got.Status.Phase != niov1alpha1.NixosConfigPhaseDegraded {
		t.Errorf("phase = %q, want Degraded after exhausting install retries (retries=%d)", got.Status.Phase, got.Status.InstallRetries)
	}
	if got.Status.InstallRetries != MaxInstallRetries {
		t.Errorf("InstallRetries = %d, want capped at %d", got.Status.InstallRetries, MaxInstallRetries)
	}

	// Once terminal-Degraded, a permanently-failing install must not churn:
	// reconciling several MORE times must keep phase Degraded, must not grow the
	// retry counter past the cap, and must NOT request a requeue.
	for i := 0; i < 5; i++ {
		// Keep the failing install child present and Failed so the failure branch
		// is exercised on every extra reconcile.
		var install niov1alpha1.NixJob
		if err := c.Get(context.Background(), types.NamespacedName{Name: "web-install", Namespace: "default"}, &install); err == nil {
			if install.Status.Failed == 0 {
				install.Status.Failed = 1
				if err := c.Status().Update(context.Background(), &install); err != nil {
					t.Fatalf("seed install failure: %v", err)
				}
			}
		}
		res := smReconcileResult(t, r, "web")
		if res.RequeueAfter != 0 {
			t.Errorf("terminal Degraded must not requeue, got %+v", res)
		}
	}

	got = getConfig(t, c, "web")
	if got.Status.Phase != niov1alpha1.NixosConfigPhaseDegraded {
		t.Errorf("phase = %q, want Degraded after extra reconciles", got.Status.Phase)
	}
	if got.Status.InstallRetries != MaxInstallRetries {
		t.Errorf("InstallRetries grew past cap: %d, want %d", got.Status.InstallRetries, MaxInstallRetries)
	}
	// The failing install child must NOT be deleted/recreated once terminal.
	var install niov1alpha1.NixJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-install", Namespace: "default"}, &install); err != nil {
		t.Errorf("install child should remain (not churned) once terminal-Degraded: %v", err)
	}
}

// TestReconcile_FullInstall_AppliedTrueWhileConverging checks that once the
// full-disk install has succeeded, the Applied condition is True even while
// day-2 is still Converging (Applied = install-success OR day-2 success).
func TestReconcile_FullInstall_AppliedTrueWhileConverging(t *testing.T) {
	cfg := smConfig()
	cfg.Spec.FullInstall = true
	r, c := smReconciler(t, cfg, smMachine())

	// First reconcile: install child created, phase Installing.
	smReconcile(t, r, "web")

	// Simulate the install completing.
	var install niov1alpha1.NixJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-install", Namespace: "default"}, &install); err != nil {
		t.Fatalf("install NixJob not created: %v", err)
	}
	install.Status.Succeeded = 1
	if err := c.Status().Update(context.Background(), &install); err != nil {
		t.Fatalf("seed install status: %v", err)
	}

	// Second reconcile: install success recorded, day-2 cron created, Converging.
	smReconcile(t, r, "web")

	got := getConfig(t, c, "web")
	if !got.Status.FullDiskInstallCompleted {
		t.Fatal("FullDiskInstallCompleted not set after install success")
	}
	if got.Status.Phase != niov1alpha1.NixosConfigPhaseConverging {
		t.Fatalf("phase = %q, want Converging", got.Status.Phase)
	}
	// The day-2 cron has no LastSuccessfulTime yet, but the install succeeded,
	// so Applied must be True.
	applied := meta.FindStatusCondition(got.Status.Conditions, niov1alpha1.ConditionApplied)
	if applied == nil {
		t.Fatal("Applied condition missing")
	}
	if applied.Status != metav1.ConditionTrue {
		t.Errorf("Applied = %q while Converging after install success, want True", applied.Status)
	}
}

// TestReconcile_MachineNotDiscoverable_Blocked checks the machine gate.
func TestReconcile_MachineNotDiscoverable_Blocked(t *testing.T) {
	m := smMachine()
	m.Status.Discoverable = false
	r, c := smReconciler(t, smConfig(), m)

	smReconcile(t, r, "web")

	got := getConfig(t, c, "web")
	if got.Status.Phase != niov1alpha1.NixosConfigPhaseBlocked {
		t.Errorf("phase = %q, want Blocked", got.Status.Phase)
	}
	// No children created.
	var cron niov1alpha1.NixCronJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-day2", Namespace: "default"}, &cron); err == nil {
		t.Error("day-2 cron must not be created for a non-discoverable machine")
	}
}

// TestReconcile_Uniqueness_SecondConfigBlocked checks that a second config for
// the same machine (later creationTimestamp) is Blocked and drives no children.
func TestReconcile_Uniqueness_SecondConfigBlocked(t *testing.T) {
	first := smConfig()
	first.Name = "web-first"
	first.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))

	second := smConfig()
	second.Name = "web-second"
	second.CreationTimestamp = metav1.NewTime(time.Now())

	r, c := smReconciler(t, first, second, smMachine())

	smReconcile(t, r, "web-second")

	got := getConfig(t, c, "web-second")
	if got.Status.Phase != niov1alpha1.NixosConfigPhaseBlocked {
		t.Errorf("second config phase = %q, want Blocked", got.Status.Phase)
	}
	// The second config must not create its own day-2 cron.
	var cron niov1alpha1.NixCronJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-second-day2", Namespace: "default"}, &cron); err == nil {
		t.Error("blocked (non-owning) config must not create a day-2 cron")
	}

	// The owning (earlier) config proceeds normally.
	smReconcile(t, r, "web-first")
	first = getConfig(t, c, "web-first")
	if first.Status.Phase != niov1alpha1.NixosConfigPhaseConverging {
		t.Errorf("owning config phase = %q, want Converging", first.Status.Phase)
	}
}

// TestReconcile_Deletion_WithOnRemove_OrphanJob checks decommission: an orphan
// NixJob (no ownerRef) is created, and the finalizer is removed on its success.
func TestReconcile_Deletion_WithOnRemove_OrphanJob(t *testing.T) {
	cfg := smConfig()
	cfg.Spec.OnRemoveFlake = "#decommission"
	cfg.Finalizers = []string{niov1alpha1.FinalizerName}
	del := metav1.NewTime(time.Now())
	cfg.DeletionTimestamp = &del

	r, c := smReconciler(t, cfg, smMachine())

	smReconcile(t, r, "web")

	// Orphan decommission NixJob created, with NO ownerRef.
	var job niov1alpha1.NixJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-onremove", Namespace: "default"}, &job); err != nil {
		t.Fatalf("decommission NixJob not created: %v", err)
	}
	if len(job.OwnerReferences) != 0 {
		t.Errorf("decommission job must be orphan (no ownerRef), got %v", job.OwnerReferences)
	}
	if job.Labels[LabelOperation] != operationDecommission {
		t.Errorf("decommission job missing operation label: %v", job.Labels)
	}
	if job.Spec.JobTemplate == nil || job.Spec.JobTemplate.TTLSecondsAfterFinished == nil {
		t.Error("decommission job must set ttlSecondsAfterFinished")
	}

	// Config still present (finalizer held), phase Removing.
	got := getConfig(t, c, "web")
	if got.Status.Phase != niov1alpha1.NixosConfigPhaseRemoving {
		t.Errorf("phase = %q, want Removing", got.Status.Phase)
	}

	// Simulate decommission success -> finalizer removed -> object gone.
	job.Status.Succeeded = 1
	if err := c.Status().Update(context.Background(), &job); err != nil {
		t.Fatalf("seed decommission success: %v", err)
	}

	smReconcile(t, r, "web")

	var after niov1alpha1.NixosConfiguration
	err := c.Get(context.Background(), types.NamespacedName{Name: "web", Namespace: "default"}, &after)
	if err == nil {
		t.Errorf("config should be gone after finalizer removal, still present with finalizers %v", after.Finalizers)
	}

	// The orphan decommission NixJob CR must be deleted before the finalizer is
	// removed, otherwise it (and its owned onremove ConfigMap) leaks forever.
	var leaked niov1alpha1.NixJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-onremove", Namespace: "default"}, &leaked); err == nil {
		t.Error("orphan decommission NixJob should be deleted on success, but it still exists")
	}
}

// TestReconcile_Deletion_WithoutOnRemove_ImmediateFinalize checks that a delete
// with no onRemoveFlake removes the finalizer immediately (no decommission job).
func TestReconcile_Deletion_WithoutOnRemove_ImmediateFinalize(t *testing.T) {
	cfg := smConfig()
	cfg.Finalizers = []string{niov1alpha1.FinalizerName}
	del := metav1.NewTime(time.Now())
	cfg.DeletionTimestamp = &del

	r, c := smReconciler(t, cfg, smMachine())

	smReconcile(t, r, "web")

	var after niov1alpha1.NixosConfiguration
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web", Namespace: "default"}, &after); err == nil {
		t.Errorf("config should be gone (finalizer removed immediately), still present: %v", after.Finalizers)
	}
	var job niov1alpha1.NixJob
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-onremove", Namespace: "default"}, &job); err == nil {
		t.Error("no decommission job should be created without onRemoveFlake")
	}
}

func containsFinalizer(finalizers []string, name string) bool {
	for _, f := range finalizers {
		if f == name {
			return true
		}
	}
	return false
}
