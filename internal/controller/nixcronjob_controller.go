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

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const kindNixCronJob = "NixCronJob"

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
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ncj.Namespace,
			Labels:    managedLabels(kindNixCronJob, ncj.Name),
		},
		Spec: cj.Spec.JobTemplate.Spec,
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
	ncj.Status.LastSuccessfulTime = cj.Status.LastSuccessfulTime
	active := make([]string, 0, len(cj.Status.Active))
	for _, ref := range cj.Status.Active {
		active = append(active, ref.Name)
	}
	ncj.Status.ActiveJobs = active

	// The CronJob is applied and pinned to the resolved revision: it is Ready as
	// scheduling infrastructure (individual runs report their own status).
	markReady(st, res.revision, ncj.Generation)
}

// SetupWithManager registers the NixCronJob controller with the manager.
func (r *NixCronJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.NixCronJob{}).
		Owns(&batchv1.CronJob{}).
		Owns(&batchv1.Job{}).
		Named("nixcronjob").
		Complete(r)
}
