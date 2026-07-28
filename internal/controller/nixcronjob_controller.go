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
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const kindNixCronJob = "NixCronJob"

// immediateJobTTLSeconds bounds how long a one-off Job fired on a revision change
// sticks around after finishing. The CronJob's history limits do not apply to it,
// so without a TTL these accumulate for the lifetime of the workload. A day is
// long enough to inspect a failure by hand.
const immediateJobTTLSeconds = 24 * 60 * 60

// NixCronJobReconciler reconciles a NixCronJob into an owned batch/v1 CronJob,
// pinning its jobTemplate to the resolved revision, and optionally firing a
// one-off Job on a new revision.
type NixCronJobReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Git      GitResolver
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixcronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixcronjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixcronjobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the shared workload flow for NixCronJob.
func (r *NixCronJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var ncj niov1alpha1.NixCronJob
	if err := r.Get(ctx, req.NamespacedName, &ncj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !ncj.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, removeFinalizer(ctx, r.Client, &ncj)
	}
	if err := ensureFinalizer(ctx, r.Client, &ncj); err != nil {
		return ctrl.Result{}, err
	}

	st := &ncj.Status.NixWorkloadStatus
	st.ObservedGeneration = ncj.Generation
	setCondition(&st.Conditions, niov1alpha1.ConditionReconciling, metav1.ConditionTrue, reasonProgressing, "reconciling", ncj.Generation)

	nix := ncj.Spec.Nix
	if nix.Suspend {
		st.Phase = niov1alpha1.PhaseSuspended
		return ctrl.Result{}, r.Status().Update(ctx, &ncj)
	}

	res, err := resolveRevision(ctx, r.Client, r.git(), ncj.Namespace, nix.Source)
	if err != nil {
		setCondition(&st.Conditions, niov1alpha1.ConditionGitSynced, metav1.ConditionFalse, reasonGitError, err.Error(), ncj.Generation)
		markStalled(st, reasonGitError, err.Error(), ncj.Generation)
		_ = r.Status().Update(ctx, &ncj)
		return ctrl.Result{RequeueAfter: infraRequeue}, nil
	}
	st.ResolvedRevision = res.revision
	st.LastPolledTime = &metav1.Time{Time: metav1.Now().Time}
	setCondition(&st.Conditions, niov1alpha1.ConditionGitSynced, metav1.ConditionTrue, reasonReady, "revision resolved", ncj.Generation)

	deps, err := resolveInfra(ctx, r.Client, r.Scheme, &ncj, nix)
	if err != nil {
		return ctrl.Result{}, err
	}
	if deps.notReady != "" {
		markStalled(st, reasonInfraNotReady, deps.notReady, ncj.Generation)
		if uerr := r.Status().Update(ctx, &ncj); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: infraRequeue}, nil
	}

	// Fire a one-off Job when the revision changed and triggerOnChange is set
	// (default false for cron), before repinning rolledOutRevision.
	revChanged := st.RolledOutRevision != res.revision
	if err := r.project(ctx, &ncj, res, deps); err != nil {
		return ctrl.Result{}, err
	}
	if revChanged && triggerOnChange(nix, false) {
		if err := r.fireImmediateJob(ctx, &ncj, res, deps); err != nil {
			log.Error(err, "failed to fire immediate Job on revision change")
		}
	}
	st.WorkloadRef = ncj.Name

	r.observe(ctx, &ncj, res)

	if err := r.Status().Update(ctx, &ncj); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: pollInterval(nix)}, nil
}

func (r *NixCronJobReconciler) git() GitResolver {
	if r.Git != nil {
		return r.Git
	}
	return ExecGitResolver{}
}

func (r *NixCronJobReconciler) project(ctx context.Context, ncj *niov1alpha1.NixCronJob, res resolvedSource, deps infraDeps) error {
	desired := r.desiredCronJob(ncj, res, deps)
	if err := controllerutil.SetControllerReference(ncj, desired, r.Scheme); err != nil {
		return err
	}
	var existing batchv1.CronJob
	err := r.Get(ctx, client.ObjectKey{Namespace: ncj.Namespace, Name: ncj.Name}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	existing.Spec = desired.Spec
	return r.Update(ctx, &existing)
}

func (r *NixCronJobReconciler) desiredCronJob(ncj *niov1alpha1.NixCronJob, res resolvedSource, deps infraDeps) *batchv1.CronJob {
	spec := *ncj.Spec.CronJobTemplate.DeepCopy()
	in := renderInput{
		spec:             ncj.Spec.Nix,
		resolvedRevision: res.revision,
		artifactURL:      res.artifactURL,
		store:            deps.store,
		builder:          deps.builder,
		sshSecretName:    deps.sshSecretName,
		kind:             kindNixCronJob,
		name:             ncj.Name,
	}
	spec.JobTemplate.Spec.Template = renderPodTemplate(in, spec.JobTemplate.Spec.Template)
	ensureBatchRestartPolicy(&spec.JobTemplate.Spec.Template)

	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ncj.Name,
			Namespace: ncj.Namespace,
			Labels:    managedLabels(kindNixCronJob, ncj.Name),
		},
		Spec: spec,
	}
}

// fireImmediateJob creates a one-off Job from the CronJob's jobTemplate on a
// revision change, honoring the native concurrencyPolicy.
func (r *NixCronJobReconciler) fireImmediateJob(ctx context.Context, ncj *niov1alpha1.NixCronJob, res resolvedSource, deps infraDeps) error {
	if ncj.Spec.CronJobTemplate.ConcurrencyPolicy == batchv1.ForbidConcurrent && len(ncj.Status.ActiveJobs) > 0 {
		return nil
	}
	rev := compositeRevision(res.revision, ncj.Spec.Nix.Run, ncj.Spec.Nix.Args)
	jobName := ncj.Name + "-" + rev + "-manual"
	var existing batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Namespace: ncj.Namespace, Name: jobName}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	cj := r.desiredCronJob(ncj, res, deps)
	jobSpec := *cj.Spec.JobTemplate.Spec.DeepCopy()
	// One-off Jobs are not covered by the CronJob's history limits, so without a
	// TTL every revision change leaves a Job behind forever. The observed
	// last-failed/last-succeeded times are monotonic, so pruning a Job never
	// resurrects a stale verdict.
	if jobSpec.TTLSecondsAfterFinished == nil {
		jobSpec.TTLSecondsAfterFinished = ptr(int32(immediateJobTTLSeconds))
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ncj.Namespace,
			Labels:    managedLabels(kindNixCronJob, ncj.Name),
		},
		Spec: jobSpec,
	}
	if err := controllerutil.SetControllerReference(ncj, job, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *NixCronJobReconciler) observe(ctx context.Context, ncj *niov1alpha1.NixCronJob, res resolvedSource) {
	st := &ncj.Status.NixWorkloadStatus
	var cj batchv1.CronJob
	if err := r.Get(ctx, client.ObjectKey{Namespace: ncj.Namespace, Name: ncj.Name}, &cj); err != nil {
		markProgressing(st, niov1alpha1.PhaseProgressing, "cronjob not yet created", ncj.Generation)
		return
	}
	ncj.Status.LastScheduleTime = cj.Status.LastScheduleTime
	active := make([]string, 0, len(cj.Status.Active))
	for _, ref := range cj.Status.Active {
		active = append(active, ref.Name)
	}
	ncj.Status.ActiveJobs = active

	// A CronJob keeps scheduling whether or not its runs succeed, so "the CronJob
	// exists" is not a health signal on its own. Look at the runs themselves.
	// Failures and successes MUST be read from the same population: the CronJob's
	// own lastSuccessfulTime covers only the Jobs it scheduled, so a one-off
	// triggerOnChange run could otherwise degrade the workload without ever being
	// able to clear it again.
	lastFailed, lastSucceeded := r.lastFinishedRuns(ctx, ncj, &cj)
	ncj.Status.LastFailedTime = lastFailed
	ncj.Status.LastSuccessfulTime = lastSucceeded

	if failing, msg := latestRunFailed(lastFailed, lastSucceeded); failing {
		st.Phase = niov1alpha1.PhaseDegraded
		setCondition(&st.Conditions, niov1alpha1.ConditionReady, metav1.ConditionFalse,
			reasonRunFailed, msg, ncj.Generation)
		st.RolledOutRevision = res.revision
		// The run happened, so whatever stalled an earlier reconcile is resolved.
		// Leaving that condition behind would keep consumers reading a stale
		// diagnosis (and, for an infra stall, outrank this failure entirely).
		clearStalled(st)
		return
	}

	// The CronJob is applied, pinned to the resolved revision, and its last
	// finished run did not fail.
	markReady(st, res.revision, ncj.Generation)
}

// lastFinishedRuns returns the completion times of the most recent failed and
// most recent successful run of this NixCronJob, counting both the Jobs the
// projected CronJob scheduled and the one-off Jobs fired on a revision change.
// Both come from the same scan, so "did the latest run fail?" is answerable.
func (r *NixCronJobReconciler) lastFinishedRuns(
	ctx context.Context, ncj *niov1alpha1.NixCronJob, cj *batchv1.CronJob,
) (lastFailed, lastSucceeded *metav1.Time) {
	// Both signals are monotonic: they only ever move forward. Jobs are pruned by
	// history limits and TTLs, and forgetting a failure because its Job was
	// garbage-collected would flip a broken workload back to Ready.
	lastFailed = ncj.Status.LastFailedTime
	lastSucceeded = ncj.Status.LastSuccessfulTime
	// The CronJob controller's own bookkeeping is a valid success signal too.
	if later(lastSucceeded, cj.Status.LastSuccessfulTime) {
		lastSucceeded = cj.Status.LastSuccessfulTime
	}

	// Served from the manager's Job cache (the controller already Owns Jobs), so
	// this is an in-memory scan rather than an API call per reconcile.
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(ncj.Namespace)); err != nil {
		// Observation only: an unreadable Job list must not flip the phase.
		return lastFailed, lastSucceeded
	}

	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !ownedBy(job, ncj.UID) && !ownedBy(job, cj.UID) {
			continue
		}
		if at := jobFinishedAt(job, batchv1.JobFailed); at != nil && later(lastFailed, at) {
			lastFailed = at
		}
		if at := jobFinishedAt(job, batchv1.JobComplete); at != nil && later(lastSucceeded, at) {
			lastSucceeded = at
		}
	}
	return lastFailed, lastSucceeded
}

// jobFinishedAt returns when the Job reached the given terminal condition, or nil
// if it did not.
func jobFinishedAt(job *batchv1.Job, want batchv1.JobConditionType) *metav1.Time {
	cond := findJobCondition(job, want)
	if cond == nil || cond.Status != corev1.ConditionTrue {
		return nil
	}
	if job.Status.CompletionTime != nil {
		return job.Status.CompletionTime.DeepCopy()
	}
	return cond.LastTransitionTime.DeepCopy()
}

// later reports whether candidate is newer than current (nil current loses).
func later(current, candidate *metav1.Time) bool {
	return current == nil || current.Before(candidate)
}

// latestRunFailed reports whether the most recent finished run failed, i.e. a
// failure exists and no success happened after it.
func latestRunFailed(lastFailed, lastSuccessful *metav1.Time) (bool, string) {
	if lastFailed == nil {
		return false, ""
	}
	if lastSuccessful != nil && !lastSuccessful.Before(lastFailed) {
		return false, ""
	}
	return true, "the most recent run failed at " + lastFailed.UTC().Format(time.RFC3339)
}

// ownedBy reports whether obj carries an ownerReference with the given UID.
func ownedBy(obj client.Object, owner types.UID) bool {
	if owner == "" {
		return false
	}
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner {
			return true
		}
	}
	return false
}

// findJobCondition returns the named Job condition, or nil.
func findJobCondition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == t {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

// SetupWithManager registers the NixCronJob controller with the manager.
func (r *NixCronJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := registerWorkloadIndexes(mgr, &niov1alpha1.NixCronJob{}, func(o client.Object) *niov1alpha1.NixSource {
		return &o.(*niov1alpha1.NixCronJob).Spec.Nix.Source
	}); err != nil {
		return err
	}
	b := ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.NixCronJob{}).
		Owns(&batchv1.CronJob{}).
		Owns(&batchv1.Job{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			enqueueByIndex(r.Client, &niov1alpha1.NixCronJobList{}, IndexByCredentialsSecret))).
		Named("nixcronjob")
	addFluxSourceWatches(b, mgr, r.Client, &niov1alpha1.NixCronJobList{})
	return b.Complete(r)
}
