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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
	"github.com/kitsunoff/nixos-operator/internal/applyjob"
	"github.com/kitsunoff/nixos-operator/internal/metrics"
)

const (
	// RequeueInterval is the default requeue interval for pending operations.
	RequeueInterval = 30 * time.Second

	// MaxConcurrentJobs is the maximum number of concurrent apply jobs.
	MaxConcurrentJobs = 5

	// JobPendingTimeout is the timeout for jobs stuck in pending state.
	JobPendingTimeout = 5 * time.Minute

	// DefaultJobTimeout is the default timeout for nixos-rebuild jobs.
	DefaultJobTimeout = 30 * time.Minute

	// FullInstallJobTimeout is the timeout for nixos-anywhere jobs.
	FullInstallJobTimeout = 60 * time.Minute

	// MaxOnRemoveRetries is the maximum number of retries for onRemoveFlake.
	MaxOnRemoveRetries = 3

	// IndexConfigByMachine is the field index for machine references.
	IndexConfigByMachine = "spec.machineRef.name"

	// LabelMachineName is the label for machine name on Jobs.
	LabelMachineName = "nio.homystack.com/machine"

	// LabelConfigName is the label for config name on Jobs.
	LabelConfigName = "nio.homystack.com/config"

	// AnnotationOnRemoveRetries tracks deletion retries.
	AnnotationOnRemoveRetries = "nio.homystack.com/on-remove-retries"

	// AnnotationResolvedRevision records the immutable commit SHA an apply Job
	// was created for, so success handling can persist the SHA (not the ref
	// name) as the applied commit.
	AnnotationResolvedRevision = "nio.homystack.com/resolved-revision"

	// DefaultApplyImage is the default container image for apply jobs.
	DefaultApplyImage = "ghcr.io/homystack/nixos-operator:latest"
)

// NixosConfigurationReconciler reconciles a NixosConfiguration object.
type NixosConfigurationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// Git resolves a mutable ref to an immutable commit SHA. Defaults to
	// ExecGitResolver when nil; tests substitute a fake.
	Git GitResolver
}

// git returns the configured GitResolver, defaulting to the production
// ExecGitResolver.
func (r *NixosConfigurationReconciler) git() GitResolver {
	if r.Git != nil {
		return r.Git
	}
	return ExecGitResolver{}
}

// resolveConfigRevision resolves the configuration's git ref to an immutable
// commit SHA. A bare ref name (e.g. "main") never changes, so without this a
// new commit pushed to the same branch is never detected.
func (r *NixosConfigurationReconciler) resolveConfigRevision(ctx context.Context, config *niov1alpha1.NixosConfiguration) (string, error) {
	ref := config.Spec.Ref
	if ref == "" {
		ref = defaultGitRef
	}
	return r.git().LsRemote(ctx, config.Spec.GitRepo, ref)
}

// resolveAdditionalFiles turns the spec's AdditionalFiles into concrete
// path/content pairs the apply Job can inject. Only Inline is delivered today;
// SecretRef and NixosFacter fail loudly rather than being silently dropped.
func resolveAdditionalFiles(config *niov1alpha1.NixosConfiguration) ([]applyjob.AdditionalFile, error) {
	if len(config.Spec.AdditionalFiles) == 0 {
		return nil, nil
	}
	files := make([]applyjob.AdditionalFile, 0, len(config.Spec.AdditionalFiles))
	for _, f := range config.Spec.AdditionalFiles {
		switch f.ValueType {
		case niov1alpha1.AdditionalFileValueTypeInline:
			files = append(files, applyjob.AdditionalFile{Path: f.Path, Content: f.Inline})
		default:
			return nil, fmt.Errorf(
				"additionalFile %q: valueType %q is not yet delivered to the apply Job (only Inline is supported)",
				f.Path, f.ValueType)
		}
	}
	return files, nil
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixosconfigurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixosconfigurations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixosconfigurations/finalizers,verbs=update
// +kubebuilder:rbac:groups=nio.homystack.com,resources=machines,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=machines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods;pods/log,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main reconciliation loop for NixosConfiguration resources.
func (r *NixosConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the NixosConfiguration instance
	var config niov1alpha1.NixosConfiguration
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Set observedGeneration immediately
	config.Status.ObservedGeneration = config.Generation

	// Set Reconciling condition to True
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionReconciling,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: config.Generation,
		Reason:             niov1alpha1.ReasonProgressing,
		Message:            "Reconciliation in progress",
	})

	// Update status early
	if err := r.Status().Update(ctx, &config); err != nil {
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !config.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &config)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&config, niov1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(&config, niov1alpha1.FinalizerName)
		if err := r.Update(ctx, &config); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Perform reconciliation
	result, reconcileErr := r.reconcile(ctx, &config)

	// Set final conditions based on result
	if reconcileErr != nil {
		log.Error(reconcileErr, "reconciliation failed")
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionStalled,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: config.Generation,
			Reason:             niov1alpha1.ReasonFailed,
			Message:            reconcileErr.Error(),
		})
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: config.Generation,
			Reason:             niov1alpha1.ReasonFailed,
			Message:            reconcileErr.Error(),
		})
	} else {
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionReconciling,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: config.Generation,
			Reason:             niov1alpha1.ReasonSucceeded,
			Message:            "Reconciliation completed",
		})
	}

	// Final status update
	if err := r.Status().Update(ctx, &config); err != nil {
		return ctrl.Result{}, err
	}

	return result, reconcileErr
}

// reconcile performs the main reconciliation logic.
func (r *NixosConfigurationReconciler) reconcile(ctx context.Context, config *niov1alpha1.NixosConfiguration) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Get the referenced Machine
	var machine niov1alpha1.Machine
	machineKey := types.NamespacedName{
		Name:      config.Spec.MachineRef.Name,
		Namespace: config.Namespace,
	}
	if err := r.Get(ctx, machineKey, &machine); err != nil {
		if apierrors.IsNotFound(err) {
			meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
				Type:               niov1alpha1.ConditionReady,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: config.Generation,
				Reason:             niov1alpha1.ReasonMachineNotReady,
				Message:            fmt.Sprintf("Machine %q not found", config.Spec.MachineRef.Name),
			})
			r.Recorder.Event(config, corev1.EventTypeWarning, "MachineNotFound",
				fmt.Sprintf("Machine %q not found", config.Spec.MachineRef.Name))
			return ctrl.Result{RequeueAfter: RequeueInterval}, nil
		}
		return ctrl.Result{}, err
	}

	// Check if Machine is discoverable
	if !machine.Status.Discoverable {
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: config.Generation,
			Reason:             niov1alpha1.ReasonMachineNotReady,
			Message:            fmt.Sprintf("Machine %q is not reachable via SSH", machine.Name),
		})
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}

	config.Status.TargetMachine = machine.Name

	// Check for existing job for this config
	existingJob, err := r.findExistingJob(ctx, config)
	if err != nil {
		return ctrl.Result{}, err
	}

	if existingJob != nil {
		// Monitor existing job
		return r.monitorJob(ctx, config, existingJob, &machine)
	}

	// Resolve the mutable ref to an immutable commit SHA so a new commit on the
	// same branch is detected (the bare ref name never changes). The controller
	// has no git credentials mounted (only the credentialed apply Job does), so
	// resolution can fail for private repos. That must NOT block apply: on
	// failure we degrade to the pre-SHA behavior (empty resolvedRev disables the
	// revision check in needsApply) and let the credentialed Job proceed.
	resolvedRev, err := r.resolveConfigRevision(ctx, config)
	if err != nil {
		effectiveRef := config.Spec.Ref
		if effectiveRef == "" {
			effectiveRef = defaultGitRef
		}
		log.Info("git ref resolution failed; continuing with degraded drift detection",
			"ref", effectiveRef, "error", err.Error())
		r.Recorder.Event(config, corev1.EventTypeWarning, "GitResolveDegraded",
			fmt.Sprintf("could not resolve ref %q to a SHA (private repo without controller credentials?); "+
				"drift detection falls back to spec-hash comparison", effectiveRef))
		resolvedRev = ""
	} else {
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionGitSynced,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: config.Generation,
			Reason:             niov1alpha1.ReasonGitCloneSucceeded,
			Message:            fmt.Sprintf("resolved ref %q to %s", config.Spec.Ref, resolvedRev),
		})
	}

	// Check if we need to apply configuration
	needsApply, reason := r.needsApply(ctx, config, &machine, resolvedRev)
	if !needsApply {
		log.Info("configuration is up to date", "reason", reason)
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionApplied,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: config.Generation,
			Reason:             niov1alpha1.ReasonConfigApplied,
			Message:            "Configuration is applied and up to date",
		})
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionReady,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: config.Generation,
			Reason:             niov1alpha1.ReasonSucceeded,
			Message:            "Configuration is applied and up to date",
		})
		meta.RemoveStatusCondition(&config.Status.Conditions, niov1alpha1.ConditionStalled)
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}

	log.Info("configuration needs apply", "reason", reason)

	// Check concurrency limits
	if hasActive, err := r.hasActiveJobForMachine(ctx, &machine); err != nil {
		return ctrl.Result{}, err
	} else if hasActive {
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionReconciling,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: config.Generation,
			Reason:             niov1alpha1.ReasonMachineInUse,
			Message:            fmt.Sprintf("Another configuration is being applied to machine %q", machine.Name),
		})
		r.Recorder.Event(config, corev1.EventTypeNormal, "MachineInUse",
			fmt.Sprintf("Waiting for another configuration to finish on machine %q", machine.Name))
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}

	// Check global concurrency limit
	activeCount, err := r.countActiveJobs(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if activeCount >= MaxConcurrentJobs {
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionReconciling,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: config.Generation,
			Reason:             niov1alpha1.ReasonQueued,
			Message:            fmt.Sprintf("Waiting in queue (active jobs: %d/%d)", activeCount, MaxConcurrentJobs),
		})
		r.Recorder.Event(config, corev1.EventTypeNormal, "Queued",
			fmt.Sprintf("Waiting in queue (active jobs: %d/%d)", activeCount, MaxConcurrentJobs))
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}

	// Create apply job
	job, err := r.createAndSubmitApplyJob(ctx, config, &machine, resolvedRev)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Info("created apply job", "job", job.Name)
	r.Recorder.Event(config, corev1.EventTypeNormal, "ApplyStarted",
		fmt.Sprintf("Started apply job %q", job.Name))

	// Update operation state
	config.Status.OperationState = &niov1alpha1.OperationState{
		Type:      r.getOperationType(config),
		StartedAt: metav1.Now(),
		Phase:     "Starting",
		JobName:   job.Name,
	}

	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionReconciling,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: config.Generation,
		Reason:             niov1alpha1.ReasonApplyStarted,
		Message:            fmt.Sprintf("Apply job %q started", job.Name),
	})

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// monitorJob monitors the progress of an existing job.
func (r *NixosConfigurationReconciler) monitorJob(ctx context.Context, config *niov1alpha1.NixosConfiguration, job *batchv1.Job, machine *niov1alpha1.Machine) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check job status
	if job.Status.Succeeded > 0 {
		log.Info("job succeeded", "job", job.Name)
		return r.handleJobSuccess(ctx, config, job, machine)
	}

	if job.Status.Failed > 0 {
		log.Info("job failed", "job", job.Name)
		return r.handleJobFailure(ctx, config, job)
	}

	// Job is still running - check for pending timeout
	if job.Status.Active == 0 && job.Status.Succeeded == 0 && job.Status.Failed == 0 {
		// Job hasn't started yet - check timeout
		if time.Since(job.CreationTimestamp.Time) > JobPendingTimeout {
			log.Info("job stuck in pending state", "job", job.Name)
			meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
				Type:               niov1alpha1.ConditionStalled,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: config.Generation,
				Reason:             niov1alpha1.ReasonJobPending,
				Message:            fmt.Sprintf("Job %q stuck in pending state for more than %v", job.Name, JobPendingTimeout),
			})
		}
	}

	// Update operation state with progress
	if config.Status.OperationState != nil {
		config.Status.OperationState.Phase = "Running"
	}

	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionReconciling,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: config.Generation,
		Reason:             niov1alpha1.ReasonApplyInProgress,
		Message:            fmt.Sprintf("Apply job %q is running", job.Name),
	})

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleJobSuccess handles successful job completion.
func (r *NixosConfigurationReconciler) handleJobSuccess(ctx context.Context, config *niov1alpha1.NixosConfiguration, job *batchv1.Job, machine *niov1alpha1.Machine) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Record metrics
	operation := "rebuild"
	if config.Spec.FullInstall && !config.Status.FullDiskInstallCompleted {
		operation = "anywhere"
	}
	if job.Status.CompletionTime != nil && job.Status.StartTime != nil {
		duration := job.Status.CompletionTime.Sub(job.Status.StartTime.Time).Seconds()
		metrics.RecordJobCompletion(operation, true, duration)
	}

	// Calculate configuration hash for change detection
	configHash := r.calculateConfigHash(config)

	// Persist the immutable SHA the Job was created for (recorded as an
	// annotation), not the mutable ref name. Fall back to the ref for Jobs
	// created before this annotation existed.
	appliedRev := job.Annotations[AnnotationResolvedRevision]
	if appliedRev == "" {
		appliedRev = config.Spec.Ref
	}

	// Update config status
	config.Status.AppliedCommit = appliedRev
	config.Status.LastAppliedTime = &metav1.Time{Time: time.Now()}
	config.Status.ConfigurationHash = configHash
	config.Status.OperationState = nil

	if config.Spec.FullInstall && !config.Status.FullDiskInstallCompleted {
		config.Status.FullDiskInstallCompleted = true
	}

	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionApplied,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: config.Generation,
		Reason:             niov1alpha1.ReasonConfigApplied,
		Message:            "Configuration applied successfully",
	})
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: config.Generation,
		Reason:             niov1alpha1.ReasonSucceeded,
		Message:            "Configuration applied successfully",
	})
	meta.RemoveStatusCondition(&config.Status.Conditions, niov1alpha1.ConditionStalled)
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionReconciling,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: config.Generation,
		Reason:             niov1alpha1.ReasonSucceeded,
		Message:            "Reconciliation completed",
	})

	// Update Machine status
	machine.Status.HasConfiguration = true
	machine.Status.AppliedConfiguration = config.Name
	machine.Status.AppliedCommit = appliedRev
	machine.Status.LastAppliedTime = config.Status.LastAppliedTime

	if err := r.Status().Update(ctx, machine); err != nil {
		log.Error(err, "failed to update machine status")
		// Don't return error - config update is more important
	}

	r.Recorder.Event(config, corev1.EventTypeNormal, "Applied",
		fmt.Sprintf("Configuration applied successfully via job %q", job.Name))

	return ctrl.Result{RequeueAfter: RequeueInterval}, nil
}

// handleJobFailure handles failed job completion.
func (r *NixosConfigurationReconciler) handleJobFailure(ctx context.Context, config *niov1alpha1.NixosConfiguration, job *batchv1.Job) (ctrl.Result, error) {
	_ = ctx // ctx reserved for future use (e.g., fetching pod logs)

	// Record metrics
	operation := "rebuild"
	if config.Spec.FullInstall && !config.Status.FullDiskInstallCompleted {
		operation = "anywhere"
	}
	if job.Status.StartTime != nil {
		duration := time.Since(job.Status.StartTime.Time).Seconds()
		metrics.RecordJobCompletion(operation, false, duration)
	}
	metrics.RecordError("nix")

	// Get failure reason from job conditions
	failureMessage := "Apply job failed"
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			if condition.Message != "" {
				failureMessage = condition.Message
			}
			break
		}
	}

	config.Status.OperationState = nil

	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionApplied,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: config.Generation,
		Reason:             niov1alpha1.ReasonApplyFailed,
		Message:            failureMessage,
	})
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionStalled,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: config.Generation,
		Reason:             niov1alpha1.ReasonApplyFailed,
		Message:            failureMessage,
	})
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: config.Generation,
		Reason:             niov1alpha1.ReasonApplyFailed,
		Message:            failureMessage,
	})

	r.Recorder.Event(config, corev1.EventTypeWarning, "ApplyFailed",
		fmt.Sprintf("Apply job %q failed: %s", job.Name, failureMessage))

	return ctrl.Result{RequeueAfter: RequeueInterval}, nil
}

// reconcileDelete handles deletion of the NixosConfiguration resource.
func (r *NixosConfigurationReconciler) reconcileDelete(ctx context.Context, config *niov1alpha1.NixosConfiguration) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("handling configuration deletion")

	// Cancel any running jobs
	if err := r.cancelRunningJobs(ctx, config); err != nil {
		log.Error(err, "failed to cancel running jobs")
	}

	// Apply onRemoveFlake if specified
	if config.Spec.OnRemoveFlake != "" {
		result, err := r.applyOnRemoveFlake(ctx, config)
		if err != nil || result.RequeueAfter > 0 {
			return result, err
		}
	}

	// Clear Machine status
	var machine niov1alpha1.Machine
	machineKey := types.NamespacedName{
		Name:      config.Spec.MachineRef.Name,
		Namespace: config.Namespace,
	}
	if err := r.Get(ctx, machineKey, &machine); err == nil {
		if machine.Status.AppliedConfiguration == config.Name {
			machine.Status.HasConfiguration = false
			machine.Status.AppliedConfiguration = ""
			machine.Status.AppliedCommit = ""
			machine.Status.LastAppliedTime = nil

			if err := r.Status().Update(ctx, &machine); err != nil {
				log.Error(err, "failed to clear machine status")
			}
		}
	}

	// Remove finalizer
	if controllerutil.ContainsFinalizer(config, niov1alpha1.FinalizerName) {
		controllerutil.RemoveFinalizer(config, niov1alpha1.FinalizerName)
		if err := r.Update(ctx, config); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// findExistingJob finds an existing job for this configuration.
func (r *NixosConfigurationReconciler) findExistingJob(ctx context.Context, config *niov1alpha1.NixosConfiguration) (*batchv1.Job, error) {
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList,
		client.InNamespace(config.Namespace),
		client.MatchingLabels{LabelConfigName: config.Name},
	); err != nil {
		return nil, err
	}

	for i := range jobList.Items {
		job := &jobList.Items[i]
		// Find active or recently completed job
		if job.Status.Active > 0 || (job.Status.Succeeded == 0 && job.Status.Failed == 0) {
			return job, nil
		}
	}

	return nil, nil
}

// needsApply determines if configuration needs to be applied.
func (r *NixosConfigurationReconciler) needsApply(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine, resolvedRev string) (bool, string) {
	_ = ctx // ctx reserved for future use (e.g., checking external state)

	// First time application
	if config.Status.AppliedCommit == "" {
		return true, "never applied"
	}

	// Full install not yet done
	if config.Spec.FullInstall && !config.Status.FullDiskInstallCompleted {
		return true, "full install not completed"
	}

	// A new commit on the same ref changes the resolved SHA even though the ref
	// name (and thus the config hash) is unchanged.
	if resolvedRev != "" && config.Status.AppliedCommit != resolvedRev {
		return true, "revision changed"
	}

	// Configuration hash changed
	currentHash := r.calculateConfigHash(config)
	if config.Status.ConfigurationHash != currentHash {
		return true, "configuration changed"
	}

	// Machine doesn't have this config applied
	if machine.Status.AppliedConfiguration != config.Name {
		return true, "machine configuration mismatch"
	}

	return false, "up to date"
}

// hasActiveJobForMachine checks if there's an active job for the machine.
func (r *NixosConfigurationReconciler) hasActiveJobForMachine(ctx context.Context, machine *niov1alpha1.Machine) (bool, error) {
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList,
		client.InNamespace(machine.Namespace),
		client.MatchingLabels{LabelMachineName: machine.Name},
	); err != nil {
		return false, err
	}

	for _, job := range jobList.Items {
		if job.Status.Active > 0 || (job.Status.Succeeded == 0 && job.Status.Failed == 0) {
			return true, nil
		}
	}

	return false, nil
}

// countActiveJobs counts the total number of active apply jobs.
func (r *NixosConfigurationReconciler) countActiveJobs(ctx context.Context) (int, error) {
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList,
		client.HasLabels{LabelConfigName},
	); err != nil {
		return 0, err
	}

	count := 0
	for _, job := range jobList.Items {
		if job.Status.Active > 0 || (job.Status.Succeeded == 0 && job.Status.Failed == 0) {
			count++
		}
	}

	return count, nil
}

// createApplyJob creates a Kubernetes Job to apply the configuration.
//
//nolint:unparam // ctx reserved for future use (e.g., fetching secrets for job spec)
func (r *NixosConfigurationReconciler) createApplyJob(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine, resolvedRev string) (*batchv1.Job, error) {
	_ = ctx // ctx reserved for future use
	jobName := fmt.Sprintf("%s-apply-%d", config.Name, time.Now().Unix())

	// Determine operation and timeout.
	operation := "NixosRebuild"
	jobTimeout := DefaultJobTimeout
	if config.Spec.FullInstall && !config.Status.FullDiskInstallCompleted {
		operation = "FullInstall"
		jobTimeout = FullInstallJobTimeout
	}
	timeout := int64(jobTimeout.Seconds())

	// Get image from jobTemplate or use default
	image := DefaultApplyImage
	if config.Spec.JobTemplate != nil && config.Spec.JobTemplate.Image != "" {
		image = config.Spec.JobTemplate.Image
	}

	// The apply binary reads its configuration from NIO_* environment variables
	// (see cmd/apply.LoadConfigFromEnv), not CLI flags.
	env := []corev1.EnvVar{
		{Name: "NIO_CONFIG_NAME", Value: config.Name},
		{Name: "NIO_CONFIG_NAMESPACE", Value: config.Namespace},
		{Name: "NIO_OPERATION", Value: operation},
		{Name: "NIO_GIT_REPO", Value: config.Spec.GitRepo},
		{Name: "NIO_GIT_REF", Value: config.Spec.Ref},
		{Name: "NIO_FLAKE", Value: config.Spec.Flake},
		{Name: "NIO_CONFIG_SUBDIR", Value: config.Spec.ConfigurationSubdir},
		{Name: "NIO_TARGET_HOST", Value: machine.Spec.Host},
		{Name: "NIO_SSH_USER", Value: machine.Spec.SSHUser},
		{Name: "NIO_TIMEOUT", Value: jobTimeout.String()},
	}

	// Resolve and pass additional files. Without this the apply binary reads an
	// empty NIO_ADDITIONAL_FILES and the declared files are silently dropped.
	additionalFiles, err := resolveAdditionalFiles(config)
	if err != nil {
		return nil, err
	}
	if len(additionalFiles) > 0 {
		encoded, err := json.Marshal(additionalFiles)
		if err != nil {
			return nil, fmt.Errorf("marshal additional files: %w", err)
		}
		env = append(env, corev1.EnvVar{Name: "NIO_ADDITIONAL_FILES", Value: string(encoded)})
	}

	// Build volumes for secrets
	volumes := []corev1.Volume{}
	volumeMounts := []corev1.VolumeMount{}

	// Mount SSH key secret if specified
	if machine.Spec.SSHKeySecretRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "ssh-key",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  machine.Spec.SSHKeySecretRef.Name,
					DefaultMode: ptr(int32(0o400)),
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "ssh-key",
			MountPath: "/secrets/ssh",
			ReadOnly:  true,
		})
		env = append(env, corev1.EnvVar{Name: "NIO_SSH_KEY_PATH", Value: "/secrets/ssh/ssh-privatekey"})
	}

	// Mount git credentials if specified
	if config.Spec.CredentialsRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "git-credentials",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  config.Spec.CredentialsRef.Name,
					DefaultMode: ptr(int32(0o400)),
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "git-credentials",
			MountPath: "/secrets/git",
			ReadOnly:  true,
		})
	}

	// Add workspace volume for git clone
	volumes = append(volumes, corev1.Volume{
		Name: "workspace",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "workspace",
		MountPath: "/workspace",
	})

	// Build pod template
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{
			{
				Name:         "apply",
				Image:        image,
				Command:      []string{"/manager", "apply"},
				Env:          env,
				VolumeMounts: volumeMounts,
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             ptr(true),
					RunAsUser:                ptr(int64(1000)),
					ReadOnlyRootFilesystem:   ptr(true),
					AllowPrivilegeEscalation: ptr(false),
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
				},
			},
		},
		Volumes: volumes,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: ptr(true),
			RunAsUser:    ptr(int64(1000)),
			FSGroup:      ptr(int64(1000)),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
	}

	// Apply jobTemplate settings
	if config.Spec.JobTemplate != nil {
		if config.Spec.JobTemplate.NodeSelector != nil {
			podSpec.NodeSelector = config.Spec.JobTemplate.NodeSelector
		}
		if config.Spec.JobTemplate.Tolerations != nil {
			podSpec.Tolerations = config.Spec.JobTemplate.Tolerations
		}
		if config.Spec.JobTemplate.Resources != nil {
			podSpec.Containers[0].Resources = *config.Spec.JobTemplate.Resources
		}
		if config.Spec.JobTemplate.ServiceAccountName != "" {
			podSpec.ServiceAccountName = config.Spec.JobTemplate.ServiceAccountName
		}
	}

	annotations := map[string]string{}
	if resolvedRev != "" {
		annotations[AnnotationResolvedRevision] = resolvedRev
	}

	backoffLimit := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: config.Namespace,
			Labels: map[string]string{
				LabelConfigName:  config.Name,
				LabelMachineName: machine.Name,
			},
			Annotations: annotations,
		},
		Spec: batchv1.JobSpec{
			ActiveDeadlineSeconds: &timeout,
			BackoffLimit:          &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						LabelConfigName:  config.Name,
						LabelMachineName: machine.Name,
					},
				},
				Spec: podSpec,
			},
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(config, job, r.Scheme); err != nil {
		return nil, err
	}

	return job, nil
}

// createAndSubmitApplyJob creates and submits an apply job to Kubernetes.
func (r *NixosConfigurationReconciler) createAndSubmitApplyJob(ctx context.Context, config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine, resolvedRev string) (*batchv1.Job, error) {
	job, err := r.createApplyJob(ctx, config, machine, resolvedRev)
	if err != nil {
		return nil, err
	}

	if err := r.Create(ctx, job); err != nil {
		return nil, err
	}

	return job, nil
}

// cancelRunningJobs cancels all running jobs for this configuration.
func (r *NixosConfigurationReconciler) cancelRunningJobs(ctx context.Context, config *niov1alpha1.NixosConfiguration) error {
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList,
		client.InNamespace(config.Namespace),
		client.MatchingLabels{LabelConfigName: config.Name},
	); err != nil {
		return err
	}

	for i := range jobList.Items {
		job := &jobList.Items[i]
		if job.Status.Active > 0 {
			// Delete the job to cancel it
			propagation := metav1.DeletePropagationBackground
			if err := r.Delete(ctx, job, &client.DeleteOptions{
				PropagationPolicy: &propagation,
			}); err != nil {
				return err
			}
		}
	}

	return nil
}

// applyOnRemoveFlake creates a Job to apply the onRemoveFlake configuration.
func (r *NixosConfigurationReconciler) applyOnRemoveFlake(ctx context.Context, config *niov1alpha1.NixosConfiguration) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check retry count
	retryCount := 0
	if countStr, ok := config.Annotations[AnnotationOnRemoveRetries]; ok {
		if _, err := fmt.Sscanf(countStr, "%d", &retryCount); err != nil {
			log.Error(err, "failed to parse retry count")
		}
	}

	if retryCount >= MaxOnRemoveRetries {
		log.Info("max onRemoveFlake retries exceeded, skipping", "retries", retryCount)
		r.Recorder.Event(config, corev1.EventTypeWarning, "OnRemoveFlakeFailed",
			fmt.Sprintf("Max retries (%d) exceeded for onRemoveFlake", MaxOnRemoveRetries))
		return ctrl.Result{}, nil
	}

	// Get machine
	var machine niov1alpha1.Machine
	machineKey := types.NamespacedName{
		Name:      config.Spec.MachineRef.Name,
		Namespace: config.Namespace,
	}
	if err := r.Get(ctx, machineKey, &machine); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("machine not found for onRemoveFlake, skipping")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Check if machine is discoverable
	if !machine.Status.Discoverable {
		log.Info("machine not discoverable, retrying onRemoveFlake later")
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}

	// Check for existing onRemove job
	jobName := fmt.Sprintf("%s-onremove", config.Name)
	var existingJob batchv1.Job
	jobKey := types.NamespacedName{Name: jobName, Namespace: config.Namespace}
	err := r.Get(ctx, jobKey, &existingJob)
	if err == nil {
		// Job exists - check status
		if existingJob.Status.Succeeded > 0 {
			log.Info("onRemoveFlake job completed successfully")
			return ctrl.Result{}, nil
		}
		if existingJob.Status.Failed > 0 {
			log.Info("onRemoveFlake job failed, incrementing retry count")
			// Increment retry count
			if config.Annotations == nil {
				config.Annotations = make(map[string]string)
			}
			config.Annotations[AnnotationOnRemoveRetries] = fmt.Sprintf("%d", retryCount+1)
			if err := r.Update(ctx, config); err != nil {
				return ctrl.Result{}, err
			}
			// Delete failed job to allow retry
			if err := r.Delete(ctx, &existingJob); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: RequeueInterval}, nil
		}
		// Job still running
		log.Info("onRemoveFlake job still running")
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	// Create onRemove job (similar to apply job but with OnRemoveFlake)
	log.Info("creating onRemoveFlake job", "flake", config.Spec.OnRemoveFlake)

	// Create a modified config for the onRemove job
	onRemoveConfig := config.DeepCopy()
	onRemoveConfig.Spec.Flake = config.Spec.OnRemoveFlake

	// The decommission Job is a one-shot; it does not record an applied commit,
	// so no resolved revision annotation is needed.
	job, err := r.createApplyJob(ctx, onRemoveConfig, &machine, "")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build onRemove job: %w", err)
	}

	// Override job name and add onRemove label
	job.Name = jobName
	job.Labels["nio.homystack.com/operation"] = "onRemove"

	if err := r.Create(ctx, job); err != nil {
		return ctrl.Result{}, fmt.Errorf("submit onRemove job: %w", err)
	}

	r.Recorder.Event(config, corev1.EventTypeNormal, "OnRemoveFlakeStarted",
		fmt.Sprintf("Started onRemoveFlake job with flake %s", config.Spec.OnRemoveFlake))

	return ctrl.Result{RequeueAfter: RequeueInterval}, nil
}

// calculateConfigHash calculates a hash of the configuration spec.
func (r *NixosConfigurationReconciler) calculateConfigHash(config *niov1alpha1.NixosConfiguration) string {
	h := sha256.New()
	_, _ = h.Write([]byte(config.Spec.GitRepo))
	_, _ = h.Write([]byte(config.Spec.Ref))
	_, _ = h.Write([]byte(config.Spec.Flake))
	_, _ = h.Write([]byte(config.Spec.ConfigurationSubdir))
	_, _ = fmt.Fprintf(h, "%v", config.Spec.FullInstall)
	for _, f := range config.Spec.AdditionalFiles {
		_, _ = h.Write([]byte(f.Path))
		_, _ = h.Write([]byte(f.Inline))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// getOperationType returns the operation type for the current configuration.
func (r *NixosConfigurationReconciler) getOperationType(config *niov1alpha1.NixosConfiguration) niov1alpha1.OperationType {
	if config.Spec.FullInstall && !config.Status.FullDiskInstallCompleted {
		return niov1alpha1.OperationTypeFullInstall
	}
	return niov1alpha1.OperationTypeNixosRebuild
}

// findConfigsForMachine returns reconcile requests for all NixosConfigurations that reference the given Machine.
func (r *NixosConfigurationReconciler) findConfigsForMachine(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)
	machine := obj.(*niov1alpha1.Machine)

	var configList niov1alpha1.NixosConfigurationList
	if err := r.List(ctx, &configList,
		client.InNamespace(machine.Namespace),
		client.MatchingFields{IndexConfigByMachine: machine.Name},
	); err != nil {
		log.Error(err, "failed to list configurations by machine")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(configList.Items))

	for _, config := range configList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      config.Name,
				Namespace: config.Namespace,
			},
		})
	}

	if len(requests) > 0 {
		log.Info("found configurations for machine", "machine", machine.Name, "count", len(requests))
	}

	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *NixosConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Set up field index for machine references
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
		Owns(&batchv1.Job{}).
		Watches(
			&niov1alpha1.Machine{},
			handler.EnqueueRequestsFromMapFunc(r.findConfigsForMachine),
		).
		Named("nixosconfiguration").
		Complete(r)
}

// ptr returns a pointer to the given value.
func ptr[T any](v T) *T {
	return &v
}
