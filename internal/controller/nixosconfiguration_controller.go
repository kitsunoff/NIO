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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const (
	// RequeueInterval is the default requeue interval for pending operations.
	RequeueInterval = 30 * time.Second

	// MaxInstallRetries bounds the Installing phase before holding in Degraded.
	MaxInstallRetries = 3

	// MaxOnRemoveRetries bounds decommission attempts before giving up.
	MaxOnRemoveRetries = 3

	// DecommissionTTLSeconds is set as ttlSecondsAfterFinished on the decommission
	// NixJob's inner batch Job so the finished Pod/Job is reaped. It does NOT
	// delete the orphan NixJob CR itself — reconcileRemoving deletes that CR once
	// the terminal state is observed, before removing the finalizer.
	DecommissionTTLSeconds = 600

	// IndexConfigByMachine is the field index for machine references.
	IndexConfigByMachine = "spec.machineRef.name"

	// LabelMachineName is the label for machine name on child workloads.
	LabelMachineName = "nio.homystack.com/machine"

	// LabelConfigName is the label for owning config name on child workloads.
	LabelConfigName = "nio.homystack.com/config"

	// LabelOperation distinguishes the orchestrator's child roles.
	LabelOperation = "nio.homystack.com/operation"

	// operationDecommission labels the orphan decommission NixJob.
	operationDecommission = "decommission"
)

// NixosConfigurationReconciler reconciles a NixosConfiguration object as a
// v1alpha2 orchestrator: it applies nothing itself, instead driving child
// NixJob/NixCronJob workloads through an explicit state machine.
type NixosConfigurationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixosconfigurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixosconfigurations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixosconfigurations/finalizers,verbs=update
// +kubebuilder:rbac:groups=nio.homystack.com,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=machines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixjobs/status,verbs=get
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixcronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixcronjobs/status,verbs=get
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main reconciliation loop for NixosConfiguration resources.
func (r *NixosConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var config niov1alpha1.NixosConfiguration
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !config.DeletionTimestamp.IsZero() {
		return r.reconcileRemoving(ctx, &config)
	}

	// Ensure the finalizer is present before creating any children so a delete
	// while children exist still runs decommission.
	if !controllerutil.ContainsFinalizer(&config, niov1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(&config, niov1alpha1.FinalizerName)
		if err := r.Update(ctx, &config); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	config.Status.ObservedGeneration = config.Generation

	result, reconcileErr := r.runStateMachine(ctx, &config)

	if err := r.Status().Update(ctx, &config); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	return result, reconcileErr
}

// runStateMachine advances a live (non-deleting) NixosConfiguration one step,
// mutating config.Status (phase, refs, conditions). It never persists status —
// Reconcile does the single Status().Update.
func (r *NixosConfigurationReconciler) runStateMachine(ctx context.Context, config *niov1alpha1.NixosConfiguration) (ctrl.Result, error) {
	// Uniqueness gate (Gap 3): the earliest-created config for a machine owns it.
	owner, err := r.machineOwner(ctx, config)
	if err != nil {
		return ctrl.Result{}, err
	}
	if owner != config.Name {
		msg := fmt.Sprintf("machine %q already owned by NixosConfiguration %q",
			config.Spec.MachineRef.Name, owner)
		r.setBlocked(config, niov1alpha1.ReasonMachineInUse, msg)
		r.suspendDayTwoCronIfExists(ctx, config)
		r.Recorder.Event(config, corev1.EventTypeWarning, "MachineOwned", msg)
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}

	// Machine gate.
	var machine niov1alpha1.Machine
	machineKey := types.NamespacedName{Name: config.Spec.MachineRef.Name, Namespace: config.Namespace}
	if err := r.Get(ctx, machineKey, &machine); err != nil {
		if apierrors.IsNotFound(err) {
			msg := fmt.Sprintf("Machine %q not found", config.Spec.MachineRef.Name)
			r.setBlocked(config, niov1alpha1.ReasonMachineNotReady, msg)
			r.suspendDayTwoCronIfExists(ctx, config)
			r.Recorder.Event(config, corev1.EventTypeWarning, "MachineNotFound", msg)
			return ctrl.Result{RequeueAfter: RequeueInterval}, nil
		}
		return ctrl.Result{}, err
	}
	if !machine.Status.Discoverable {
		msg := fmt.Sprintf("Machine %q is not reachable via SSH", machine.Name)
		r.setBlocked(config, niov1alpha1.ReasonMachineNotReady, msg)
		r.suspendDayTwoCronIfExists(ctx, config)
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}

	config.Status.TargetMachine = machine.Name

	// Install path.
	if config.Spec.FullInstall && !config.Status.FullDiskInstallCompleted {
		done, res, err := r.reconcileInstall(ctx, config, &machine)
		if err != nil || !done {
			return res, err
		}
		// Install finished successfully; fall through to the day-2 path.
	}

	// Day-2 path.
	return r.reconcileDayTwo(ctx, config, &machine)
}

// reconcileInstall drives the full-disk install child. It returns done=true only
// when the install has completed successfully (so the caller proceeds to day-2).
func (r *NixosConfigurationReconciler) reconcileInstall(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) (bool, ctrl.Result, error) {
	log := logf.FromContext(ctx)

	job, err := r.ensureInstallNixJob(ctx, config, machine)
	if err != nil {
		return false, ctrl.Result{}, err
	}
	config.Status.InstallJobRef = job.Name

	switch {
	case job.Status.Succeeded > 0:
		// Persist completion DURABLY before deleting the child. If the final
		// shared status update later in this reconcile conflicts (or is
		// otherwise not persisted), a subsequent reconcile must never observe
		// FullDiskInstallCompleted=false and recreate the install NixJob, which
		// would re-run nixos-anywhere and re-wipe the installed machine. Only
		// delete the child once completion is durable.
		if err := r.markFullDiskInstallCompleted(ctx, config); err != nil {
			return false, ctrl.Result{}, err
		}
		if err := r.Delete(ctx, job); err != nil && !apierrors.IsNotFound(err) {
			return false, ctrl.Result{}, err
		}
		r.Recorder.Event(config, corev1.EventTypeNormal, "InstallSucceeded",
			fmt.Sprintf("Full-disk install %q succeeded", job.Name))
		return true, ctrl.Result{}, nil

	case job.Status.Failed > 0:
		// A prior failure already triggered a delete-and-recreate; wait for the
		// deletion to finish rather than counting the same failure twice.
		if !job.DeletionTimestamp.IsZero() {
			r.setPhase(config, niov1alpha1.NixosConfigPhaseInstalling, niov1alpha1.ReasonInstalling,
				fmt.Sprintf("Full-disk install %q is being retried", job.Name), false, nil)
			return false, ctrl.Result{RequeueAfter: RequeueInterval}, nil
		}
		// Check the cap BEFORE incrementing. Once at/over the cap the install is
		// terminally Degraded: set it idempotently and return WITHOUT requeue and
		// WITHOUT deleting/recreating the child, so a permanently-failing install
		// does not churn its status/counter or storm the reconcile loop.
		if config.Status.InstallRetries >= MaxInstallRetries {
			msg := fmt.Sprintf("Full-disk install failed after %d retries", MaxInstallRetries)
			if config.Status.Phase != niov1alpha1.NixosConfigPhaseDegraded {
				r.Recorder.Event(config, corev1.EventTypeWarning, "InstallFailed", msg)
			}
			// A full-disk install that never succeeded left nothing on the machine.
			r.setDegraded(config, msg, false)
			return false, ctrl.Result{}, nil
		}
		// Under the cap: count this failure and delete-and-recreate the child.
		config.Status.InstallRetries++
		log.Info("install child failed; recreating", "job", job.Name, "retries", config.Status.InstallRetries)
		if err := r.Delete(ctx, job); err != nil && !apierrors.IsNotFound(err) {
			return false, ctrl.Result{}, err
		}
		r.setPhase(config, niov1alpha1.NixosConfigPhaseInstalling, niov1alpha1.ReasonInstalling,
			fmt.Sprintf("Retrying full-disk install (attempt %d/%d)", config.Status.InstallRetries, MaxInstallRetries), false, nil)
		return false, ctrl.Result{RequeueAfter: RequeueInterval}, nil

	default:
		r.setPhase(config, niov1alpha1.NixosConfigPhaseInstalling, niov1alpha1.ReasonInstalling,
			fmt.Sprintf("Full-disk install %q is running", job.Name), false, nil)
		return false, ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}
}

// reconcileDayTwo drives the recurring day-2 convergence child.
func (r *NixosConfigurationReconciler) reconcileDayTwo(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) (ctrl.Result, error) {
	cron, err := r.ensureDayTwoNixCronJob(ctx, config, machine, false)
	if err != nil {
		return ctrl.Result{}, err
	}
	config.Status.DayTwoCronJobRef = cron.Name
	config.Status.ResolvedRevision = cron.Status.RolledOutRevision

	gitSynced := meta.FindStatusCondition(cron.Status.Conditions, niov1alpha1.ConditionGitSynced)

	// Applied is a fact about the MACHINE, not about the latest run: a completed
	// full-disk install, or any successful day-two run, means a configuration is
	// on the host. It must survive a stalled or failing converge — otherwise a
	// one-minute network blip reports a working machine as never configured.
	applied := config.Status.FullDiskInstallCompleted || cron.Status.LastSuccessfulTime != nil

	switch {
	case cron.Status.Phase == niov1alpha1.PhaseReady && cron.Status.LastSuccessfulTime != nil:
		config.Status.LastAppliedTime = cron.Status.LastSuccessfulTime
		r.setPhase(config, niov1alpha1.NixosConfigPhaseReady, niov1alpha1.ReasonSucceeded,
			"Day-2 convergence healthy; configuration applied", true, gitSynced)
		if err := r.writeBackMachineStatus(ctx, config, machine); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil

	// A day-two cron stalled on a missing NixStore/NixBuilder never ran, so
	// claiming the run failed is wrong — and the reference name it is waiting on
	// is the only useful thing to report.
	case convergeStall(cron, true) != nil:
		r.setDegraded(config, "Day-2 convergence is stalled: "+convergeStall(cron, true).Message, applied)
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil

	case cron.Status.Phase == niov1alpha1.PhaseDegraded || cron.Status.Phase == niov1alpha1.PhaseFailed:
		r.setDegraded(config, "Day-2 convergence run failed", applied)
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil

	default:
		if cron.Status.LastSuccessfulTime != nil {
			config.Status.LastAppliedTime = cron.Status.LastSuccessfulTime
		}
		r.setPhase(config, niov1alpha1.NixosConfigPhaseConverging, niov1alpha1.ReasonConverging,
			"Day-2 convergence in progress", applied, gitSynced)
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}
}

// reconcileRemoving handles deletion: it decommissions the machine via an orphan
// NixJob (which outlives the parent), then removes the finalizer.
func (r *NixosConfigurationReconciler) reconcileRemoving(ctx context.Context, config *niov1alpha1.NixosConfiguration) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(config, niov1alpha1.FinalizerName) {
		return ctrl.Result{}, nil
	}

	// Stop day-2 convergence during decommission.
	if err := r.deleteDayTwoCronIfExists(ctx, config); err != nil {
		log.Error(err, "failed to delete day-2 cron during removal")
	}

	var machine niov1alpha1.Machine
	machineKey := types.NamespacedName{Name: config.Spec.MachineRef.Name, Namespace: config.Namespace}
	machineErr := r.Get(ctx, machineKey, &machine)
	machineFound := machineErr == nil
	if machineErr != nil && !apierrors.IsNotFound(machineErr) {
		return ctrl.Result{}, machineErr
	}

	// Nothing to decommission: clear machine writeback and finalize.
	if config.Spec.OnRemoveFlake == "" || !machineFound {
		if machineFound {
			if err := r.clearMachineStatus(ctx, config, &machine); err != nil {
				return ctrl.Result{}, err
			}
		}
		return r.finalize(ctx, config)
	}

	// Discover the orphan decommission job by label (an operator restart may have
	// lost status.decommissionJobRef).
	job, err := r.findDecommissionJob(ctx, config)
	if err != nil {
		return ctrl.Result{}, err
	}

	if job == nil {
		// Not found (never created, or self-cleaned before success was observed):
		// (re)create it. Retries are only counted on observed failures below.
		newJob, err := r.ensureDecommissionNixJob(ctx, config, &machine)
		if err != nil {
			// Cannot build/create (e.g. no SSH key): decommission is impossible,
			// so finalize rather than block deletion forever.
			r.Recorder.Event(config, corev1.EventTypeWarning, "OnRemoveFlakeSkipped",
				fmt.Sprintf("Cannot run onRemoveFlake: %v", err))
			return r.finalize(ctx, config)
		}
		config.Status.DecommissionJobRef = newJob.Name
		config.Status.Phase = niov1alpha1.NixosConfigPhaseRemoving
		if err := r.statusUpdate(ctx, config); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}

	config.Status.DecommissionJobRef = job.Name

	switch {
	case job.Status.Succeeded > 0:
		if err := r.clearMachineStatus(ctx, config, &machine); err != nil {
			return ctrl.Result{}, err
		}
		// Delete the orphan decommission NixJob CR (best-effort) BEFORE removing
		// the finalizer: the finalizer keeps this orchestrator reconciling, so we
		// are alive to clean it up. Deleting the CR cascades its owned onremove
		// ConfigMap. Its ttlSecondsAfterFinished only reaps the inner batch Job,
		// not the CR, so without this the orphan leaks on every deletion.
		if err := r.deleteDecommissionJob(ctx, job); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(config, corev1.EventTypeNormal, "OnRemoveFlakeSucceeded",
			"Decommission flake applied successfully")
		return r.finalize(ctx, config)

	case job.Status.Failed > 0:
		if job.DeletionTimestamp.IsZero() {
			// Persist the incremented retry count robustly BEFORE deleting the
			// child, so a status-update conflict cannot drop the count and let
			// decommission exceed MaxOnRemoveRetries.
			if err := r.bumpOnRemoveRetries(ctx, config); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.Delete(ctx, job); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
		if config.Status.OnRemoveRetries >= MaxOnRemoveRetries {
			r.Recorder.Event(config, corev1.EventTypeWarning, "OnRemoveFlakeFailed",
				fmt.Sprintf("Decommission failed after %d attempts; finalizing anyway", MaxOnRemoveRetries))
			if err := r.clearMachineStatus(ctx, config, &machine); err != nil {
				return ctrl.Result{}, err
			}
			// Terminal give-up: clean up the orphan CR before finalizing.
			if err := r.deleteDecommissionJob(ctx, job); err != nil {
				return ctrl.Result{}, err
			}
			return r.finalize(ctx, config)
		}
		config.Status.Phase = niov1alpha1.NixosConfigPhaseRemoving
		if err := r.statusUpdate(ctx, config); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil

	default:
		config.Status.Phase = niov1alpha1.NixosConfigPhaseRemoving
		if err := r.statusUpdate(ctx, config); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}
}

// finalize removes the finalizer, allowing the API server to delete the object.
func (r *NixosConfigurationReconciler) finalize(ctx context.Context, config *niov1alpha1.NixosConfiguration) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(config, niov1alpha1.FinalizerName)
	if err := r.Update(ctx, config); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// deleteDecommissionJob deletes the orphan decommission NixJob CR best-effort
// (a missing object is fine — it may have already been reaped).
func (r *NixosConfigurationReconciler) deleteDecommissionJob(ctx context.Context, job *niov1alpha1.NixJob) error {
	if err := r.Delete(ctx, job); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// bumpOnRemoveRetries increments status.onRemoveRetries and persists it,
// retrying on conflict by re-reading the latest object so a transient conflict
// cannot drop the count (which would let decommission exceed MaxOnRemoveRetries).
// The working copy is synced to the persisted state on success.
func (r *NixosConfigurationReconciler) bumpOnRemoveRetries(ctx context.Context, config *niov1alpha1.NixosConfiguration) error {
	key := client.ObjectKeyFromObject(config)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest niov1alpha1.NixosConfiguration
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		latest.Status.OnRemoveRetries++
		latest.Status.Phase = niov1alpha1.NixosConfigPhaseRemoving
		if err := r.Status().Update(ctx, &latest); err != nil {
			return err
		}
		config.Status = latest.Status
		config.ResourceVersion = latest.ResourceVersion
		return nil
	})
}

// markFullDiskInstallCompleted durably persists that the full-disk install has
// finished (status.fullDiskInstallCompleted=true, status.installJobRef cleared),
// retrying on conflict by re-reading the latest object. This must succeed BEFORE
// the install child is deleted so no later reconcile can observe completion as
// false and recreate the child (which would re-wipe the installed machine). The
// working copy is synced to the persisted state on success so the shared status
// update later in the same reconcile does not conflict. It is idempotent: setting
// the flag when already true is a no-op write.
func (r *NixosConfigurationReconciler) markFullDiskInstallCompleted(ctx context.Context, config *niov1alpha1.NixosConfiguration) error {
	key := client.ObjectKeyFromObject(config)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest niov1alpha1.NixosConfiguration
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		latest.Status.FullDiskInstallCompleted = true
		latest.Status.InstallJobRef = ""
		if err := r.Status().Update(ctx, &latest); err != nil {
			return err
		}
		config.Status = latest.Status
		config.ResourceVersion = latest.ResourceVersion
		return nil
	})
}

// statusUpdate persists status, treating a conflict as a soft (retryable) error.
func (r *NixosConfigurationReconciler) statusUpdate(ctx context.Context, config *niov1alpha1.NixosConfiguration) error {
	if err := r.Status().Update(ctx, config); err != nil {
		if apierrors.IsConflict(err) {
			return nil
		}
		return err
	}
	return nil
}

// machineOwner returns the name of the NixosConfiguration that owns config's
// target machine: the one with the earliest creationTimestamp (tie-break by
// name). Listing by namespace and filtering in Go avoids depending on the field
// index, which is unavailable to a direct (non-cached) client in tests.
func (r *NixosConfigurationReconciler) machineOwner(ctx context.Context, config *niov1alpha1.NixosConfiguration) (string, error) {
	var list niov1alpha1.NixosConfigurationList
	if err := r.List(ctx, &list, client.InNamespace(config.Namespace)); err != nil {
		return "", err
	}
	winner := config
	for i := range list.Items {
		c := &list.Items[i]
		if c.Spec.MachineRef.Name != config.Spec.MachineRef.Name {
			continue
		}
		if c.Name == config.Name {
			continue
		}
		if configEarlier(c, winner) {
			winner = c
		}
	}
	return winner.Name, nil
}

// configEarlier reports whether a should win ownership over b: earlier
// creationTimestamp, or the lexicographically smaller name on a tie.
func configEarlier(a, b *niov1alpha1.NixosConfiguration) bool {
	if a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.Name < b.Name
	}
	return a.CreationTimestamp.Before(&b.CreationTimestamp)
}

// childLabels are the common labels stamped on install/day-2 children.
func childLabels(config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) map[string]string {
	return map[string]string{
		LabelConfigName:  config.Name,
		LabelMachineName: machine.Name,
	}
}

// ensureInstallNixJob creates-or-updates the owned install NixJob and returns
// its current state.
func (r *NixosConfigurationReconciler) ensureInstallNixJob(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) (*niov1alpha1.NixJob, error) {
	desired, err := buildInstallNixJob(config, machine)
	if err != nil {
		return nil, err
	}
	desired.Labels = childLabels(config, machine)
	if err := controllerutil.SetControllerReference(config, desired, r.Scheme); err != nil {
		return nil, err
	}

	var existing niov1alpha1.NixJob
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		return desired, nil
	} else if err != nil {
		return nil, err
	}
	if !existing.DeletionTimestamp.IsZero() {
		return &existing, nil
	}
	existing.Labels = desired.Labels
	existing.Spec = desired.Spec
	if err := r.Update(ctx, &existing); err != nil {
		return nil, err
	}
	return &existing, nil
}

// ensureDayTwoNixCronJob creates-or-updates the owned day-2 NixCronJob (with the
// requested suspend state) and returns its current state.
func (r *NixosConfigurationReconciler) ensureDayTwoNixCronJob(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine, suspend bool) (*niov1alpha1.NixCronJob, error) {
	desired, err := buildDayTwoNixCronJob(config, machine)
	if err != nil {
		return nil, err
	}
	desired.Labels = childLabels(config, machine)
	desired.Spec.Nix.Suspend = suspend
	if err := controllerutil.SetControllerReference(config, desired, r.Scheme); err != nil {
		return nil, err
	}

	var existing niov1alpha1.NixCronJob
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		return desired, nil
	} else if err != nil {
		return nil, err
	}
	if !existing.DeletionTimestamp.IsZero() {
		return &existing, nil
	}
	existing.Labels = desired.Labels
	existing.Spec = desired.Spec
	if err := r.Update(ctx, &existing); err != nil {
		return nil, err
	}
	return &existing, nil
}

// ensureDecommissionNixJob creates the orphan decommission NixJob (NO ownerRef,
// so it survives parent deletion) if absent, and returns it.
func (r *NixosConfigurationReconciler) ensureDecommissionNixJob(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) (*niov1alpha1.NixJob, error) {
	desired, err := buildDecommissionNixJob(config, machine)
	if err != nil {
		return nil, err
	}
	desired.Labels = map[string]string{
		LabelConfigName:  config.Name,
		LabelMachineName: machine.Name,
		LabelOperation:   operationDecommission,
	}
	// buildDecommissionNixJob always sets JobTemplate; reap the finished inner
	// batch Job. Note this TTL does NOT delete the orphan NixJob CR — the
	// orchestrator deletes that CR in reconcileRemoving before finalizing.
	desired.Spec.JobTemplate.TTLSecondsAfterFinished = ptr(int32(DecommissionTTLSeconds))
	// Deliberately NO SetControllerReference (Key decision #3): an ownerRef would
	// cascade-delete the job the moment the parent is removed.

	var existing niov1alpha1.NixJob
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		r.Recorder.Event(config, corev1.EventTypeNormal, "OnRemoveFlakeStarted",
			fmt.Sprintf("Started decommission job %q", desired.Name))
		return desired, nil
	} else if err != nil {
		return nil, err
	}
	return &existing, nil
}

// findDecommissionJob discovers the orphan decommission NixJob for config by
// label, independent of status.decommissionJobRef.
func (r *NixosConfigurationReconciler) findDecommissionJob(ctx context.Context, config *niov1alpha1.NixosConfiguration) (*niov1alpha1.NixJob, error) {
	var list niov1alpha1.NixJobList
	if err := r.List(ctx, &list,
		client.InNamespace(config.Namespace),
		client.MatchingLabels{LabelConfigName: config.Name, LabelOperation: operationDecommission},
	); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	return &list.Items[0], nil
}

// suspendDayTwoCronIfExists suspends the day-2 cron (keeping run history) when
// the config is Blocked. Suspend is modeled by the workload's Nix.Suspend knob.
func (r *NixosConfigurationReconciler) suspendDayTwoCronIfExists(ctx context.Context, config *niov1alpha1.NixosConfiguration) {
	log := logf.FromContext(ctx)
	var cron niov1alpha1.NixCronJob
	key := types.NamespacedName{Name: dayTwoChildName(config.Name), Namespace: config.Namespace}
	if err := r.Get(ctx, key, &cron); err != nil {
		return
	}
	if cron.Spec.Nix.Suspend {
		return
	}
	cron.Spec.Nix.Suspend = true
	if err := r.Update(ctx, &cron); err != nil {
		log.Error(err, "failed to suspend day-2 cron")
	}
}

// deleteDayTwoCronIfExists deletes the day-2 cron so convergence stops during
// decommission.
func (r *NixosConfigurationReconciler) deleteDayTwoCronIfExists(ctx context.Context, config *niov1alpha1.NixosConfiguration) error {
	var cron niov1alpha1.NixCronJob
	key := types.NamespacedName{Name: dayTwoChildName(config.Name), Namespace: config.Namespace}
	if err := r.Get(ctx, key, &cron); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !cron.DeletionTimestamp.IsZero() {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, &cron))
}

// writeBackMachineStatus records the applied config/commit on the Machine
// (Gap 6), so `kubectl get machine` reflects what the node runs.
func (r *NixosConfigurationReconciler) writeBackMachineStatus(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) error {
	machine.Status.HasConfiguration = true
	machine.Status.AppliedConfiguration = config.Name
	machine.Status.AppliedCommit = config.Status.ResolvedRevision
	machine.Status.LastAppliedTime = config.Status.LastAppliedTime
	return r.Status().Update(ctx, machine)
}

// clearMachineStatus clears the applied-config writeback on the Machine, but
// only if it still points at this config.
func (r *NixosConfigurationReconciler) clearMachineStatus(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) error {
	if machine.Status.AppliedConfiguration != config.Name {
		return nil
	}
	machine.Status.HasConfiguration = false
	machine.Status.AppliedConfiguration = ""
	machine.Status.AppliedCommit = ""
	machine.Status.LastAppliedTime = nil
	if err := r.Status().Update(ctx, machine); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

// setPhase sets the phase and derives the standard conditions.
func (r *NixosConfigurationReconciler) setPhase(config *niov1alpha1.NixosConfiguration, phase, reason, msg string, applied bool, childGitSynced *metav1.Condition) {
	config.Status.Phase = phase
	gen := config.Generation

	ready := phase == niov1alpha1.NixosConfigPhaseReady
	stalled := phase == niov1alpha1.NixosConfigPhaseDegraded || phase == niov1alpha1.NixosConfigPhaseBlocked

	readyStatus := metav1.ConditionFalse
	if ready {
		readyStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionReady,
		Status:             readyStatus,
		ObservedGeneration: gen,
		Reason:             reason,
		Message:            msg,
	})

	if stalled {
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionStalled,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: gen,
			Reason:             reason,
			Message:            msg,
		})
	} else {
		meta.RemoveStatusCondition(&config.Status.Conditions, niov1alpha1.ConditionStalled)
	}

	reconcilingStatus := metav1.ConditionTrue
	if ready || stalled {
		reconcilingStatus = metav1.ConditionFalse
	}
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionReconciling,
		Status:             reconcilingStatus,
		ObservedGeneration: gen,
		Reason:             reason,
		Message:            msg,
	})

	appliedStatus := metav1.ConditionFalse
	appliedReason := niov1alpha1.ReasonWaiting
	if applied {
		appliedStatus = metav1.ConditionTrue
		appliedReason = niov1alpha1.ReasonConfigApplied
	}
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionApplied,
		Status:             appliedStatus,
		ObservedGeneration: gen,
		Reason:             appliedReason,
		Message:            msg,
	})

	if childGitSynced != nil {
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionGitSynced,
			Status:             childGitSynced.Status,
			ObservedGeneration: gen,
			Reason:             childGitSynced.Reason,
			Message:            childGitSynced.Message,
		})
	}
}

// setBlocked is a convenience for the Blocked phase.
func (r *NixosConfigurationReconciler) setBlocked(config *niov1alpha1.NixosConfiguration, reason, msg string) {
	r.setPhase(config, niov1alpha1.NixosConfigPhaseBlocked, reason, msg, false, nil)
}

// setDegraded is a convenience for the Degraded phase.
// setDegraded reports a Degraded phase. `applied` is passed through rather than
// assumed false: a machine that already carries a configuration keeps
// Applied=True while convergence is broken.
func (r *NixosConfigurationReconciler) setDegraded(
	config *niov1alpha1.NixosConfiguration, msg string, applied bool,
) {
	r.setPhase(config, niov1alpha1.NixosConfigPhaseDegraded, niov1alpha1.ReasonApplyFailed, msg, applied, nil)
}

// findConfigsForMachine enqueues configs referencing a changed Machine.
func (r *NixosConfigurationReconciler) findConfigsForMachine(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)
	machine, ok := obj.(*niov1alpha1.Machine)
	if !ok {
		return nil
	}

	var configList niov1alpha1.NixosConfigurationList
	if err := r.List(ctx, &configList,
		client.InNamespace(machine.Namespace),
		client.MatchingFields{IndexConfigByMachine: machine.Name},
	); err != nil {
		log.Error(err, "failed to list configurations by machine")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(configList.Items))
	for i := range configList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      configList.Items[i].Name,
				Namespace: configList.Items[i].Namespace,
			},
		})
	}
	return requests
}

// findConfigsForOrphanRemoveJob maps an orphan decommission NixJob back to its
// config via labels (the job has no ownerRef, so Owns() cannot enqueue it).
func (r *NixosConfigurationReconciler) findConfigsForOrphanRemoveJob(_ context.Context, obj client.Object) []reconcile.Request {
	job, ok := obj.(*niov1alpha1.NixJob)
	if !ok {
		return nil
	}
	if job.Labels[LabelOperation] != operationDecommission {
		return nil
	}
	cfgName := job.Labels[LabelConfigName]
	if cfgName == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: cfgName, Namespace: job.Namespace},
	}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NixosConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &niov1alpha1.NixosConfiguration{},
		IndexConfigByMachine,
		func(obj client.Object) []string {
			config := obj.(*niov1alpha1.NixosConfiguration)
			return []string{config.Spec.MachineRef.Name}
		},
	); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.NixosConfiguration{}).
		Owns(&niov1alpha1.NixJob{}).
		Owns(&niov1alpha1.NixCronJob{}).
		Watches(
			&niov1alpha1.Machine{},
			handler.EnqueueRequestsFromMapFunc(r.findConfigsForMachine),
		).
		Watches(
			&niov1alpha1.NixJob{},
			handler.EnqueueRequestsFromMapFunc(r.findConfigsForOrphanRemoveJob),
		).
		Named("nixosconfiguration").
		Complete(r)
}

// ptr returns a pointer to the given value.
func ptr[T any](v T) *T {
	return &v
}
