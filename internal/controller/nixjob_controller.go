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
	"sort"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const (
	kindNixJob = "NixJob"

	// jobHistoryLimit is how many completed run-Jobs to keep beyond the current.
	jobHistoryLimit = 3
)

// NixJobReconciler reconciles a NixJob into immutable per-revision run-Jobs.
type NixJobReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Git      GitResolver
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixjobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the shared workload flow for NixJob.
func (r *NixJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var nj niov1alpha1.NixJob
	if err := r.Get(ctx, req.NamespacedName, &nj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !nj.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, removeFinalizer(ctx, r.Client, &nj)
	}
	if err := ensureFinalizer(ctx, r.Client, &nj); err != nil {
		return ctrl.Result{}, err
	}

	st := &nj.Status.NixWorkloadStatus
	st.ObservedGeneration = nj.Generation
	setCondition(&st.Conditions, niov1alpha1.ConditionReconciling, metav1.ConditionTrue, reasonProgressing, "reconciling", nj.Generation)

	nix := nj.Spec.Nix
	if nix.Suspend {
		st.Phase = niov1alpha1.PhaseSuspended
		return ctrl.Result{}, r.Status().Update(ctx, &nj)
	}

	res, err := resolveRevision(ctx, r.Client, r.git(), nj.Namespace, nix.Source)
	if err != nil {
		setCondition(&st.Conditions, niov1alpha1.ConditionGitSynced, metav1.ConditionFalse, reasonGitError, err.Error(), nj.Generation)
		markStalled(st, reasonGitError, err.Error(), nj.Generation)
		_ = r.Status().Update(ctx, &nj)
		return ctrl.Result{RequeueAfter: infraRequeue}, nil
	}
	st.ResolvedRevision = res.revision
	st.LastPolledTime = &metav1.Time{Time: metav1.Now().Time}
	setCondition(&st.Conditions, niov1alpha1.ConditionGitSynced, metav1.ConditionTrue, reasonReady, "revision resolved", nj.Generation)

	deps, err := resolveInfra(ctx, r.Client, r.Scheme, &nj, nix)
	if err != nil {
		return ctrl.Result{}, err
	}
	if deps.notReady != "" {
		markStalled(st, reasonInfraNotReady, deps.notReady, nj.Generation)
		if uerr := r.Status().Update(ctx, &nj); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: infraRequeue}, nil
	}

	rev := compositeRevision(res.revision, nix.Run, nix.Args)
	jobName := nj.Name + "-" + rev

	if err := r.ensureRunJob(ctx, &nj, res, deps, jobName); err != nil {
		return ctrl.Result{}, err
	}
	st.WorkloadRef = jobName

	if err := r.observe(ctx, &nj, jobName); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.gcOldJobs(ctx, &nj, jobName); err != nil {
		log.Error(err, "run-Job history GC failed")
	}

	if err := r.Status().Update(ctx, &nj); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: pollInterval(nix)}, nil
}

func (r *NixJobReconciler) git() GitResolver {
	if r.Git != nil {
		return r.Git
	}
	return ExecGitResolver{}
}

// ensureRunJob creates the immutable run-Job for the current revision. With
// triggerOnChange=false the Job runs once: no new Job is created if any run-Job
// already exists. Existing Jobs are never mutated.
func (r *NixJobReconciler) ensureRunJob(ctx context.Context, nj *niov1alpha1.NixJob, res resolvedSource, deps infraDeps, jobName string) error {
	var existing batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Namespace: nj.Namespace, Name: jobName}, &existing)
	if err == nil {
		return nil // immutable: never touch an existing run-Job
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	if !triggerOnChange(nj.Spec.Nix, true) {
		// Run once: skip creating a new Job if any run-Job already exists.
		owned, listErr := r.listRunJobs(ctx, nj)
		if listErr != nil {
			return listErr
		}
		if len(owned) > 0 {
			return nil
		}
	}

	job := r.desiredJob(nj, res, deps, jobName)
	if err := controllerutil.SetControllerReference(nj, job, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *NixJobReconciler) desiredJob(nj *niov1alpha1.NixJob, res resolvedSource, deps infraDeps, jobName string) *batchv1.Job {
	var spec batchv1.JobSpec
	if nj.Spec.JobTemplate != nil {
		spec = *nj.Spec.JobTemplate.DeepCopy()
	}
	in := renderInput{
		spec:             nj.Spec.Nix,
		resolvedRevision: res.revision,
		artifactURL:      res.artifactURL,
		store:            deps.store,
		builder:          deps.builder,
		kind:             kindNixJob,
		name:             nj.Name,
	}
	spec.Template = renderPodTemplate(in, spec.Template)
	ensureBatchRestartPolicy(&spec.Template)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: nj.Namespace,
			Labels:    managedLabels(kindNixJob, nj.Name),
		},
		Spec: spec,
	}
}

// observe reads the current run-Job and maps its status onto the NixJob.
func (r *NixJobReconciler) observe(ctx context.Context, nj *niov1alpha1.NixJob, jobName string) error {
	st := &nj.Status.NixWorkloadStatus
	var job batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Namespace: nj.Namespace, Name: jobName}, &job)
	if apierrors.IsNotFound(err) {
		markProgressing(st, niov1alpha1.PhaseProgressing, "run-Job not created", nj.Generation)
		return nil
	}
	if err != nil {
		return err
	}
	nj.Status.ActiveJob = jobName
	nj.Status.Succeeded = job.Status.Succeeded
	nj.Status.Failed = job.Status.Failed
	if job.Status.StartTime != nil {
		nj.Status.LastRunTime = job.Status.StartTime
	}

	switch {
	case jobHasCondition(&job, batchv1.JobComplete):
		markReady(st, nj.Status.ResolvedRevision, nj.Generation)
	case jobHasCondition(&job, batchv1.JobFailed):
		st.Phase = niov1alpha1.PhaseFailed
		setCondition(&st.Conditions, niov1alpha1.ConditionReady, metav1.ConditionFalse, niov1alpha1.ReasonFailed, "run-Job failed", nj.Generation)
		setCondition(&st.Conditions, niov1alpha1.ConditionStalled, metav1.ConditionTrue, niov1alpha1.ReasonFailed, "run-Job exhausted its backoffLimit", nj.Generation)
	default:
		markProgressing(st, niov1alpha1.PhaseProgressing, "run-Job in progress", nj.Generation)
	}
	return nil
}

// gcOldJobs deletes completed run-Jobs beyond the history limit, keeping the
// current one and the newest jobHistoryLimit completed Jobs.
func (r *NixJobReconciler) gcOldJobs(ctx context.Context, nj *niov1alpha1.NixJob, currentName string) error {
	jobs, err := r.listRunJobs(ctx, nj)
	if err != nil {
		return err
	}
	completed := make([]batchv1.Job, 0, len(jobs))
	for _, j := range jobs {
		if j.Name == currentName {
			continue
		}
		if jobHasCondition(&j, batchv1.JobComplete) || jobHasCondition(&j, batchv1.JobFailed) {
			completed = append(completed, j)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].CreationTimestamp.After(completed[j].CreationTimestamp.Time)
	})
	for i := jobHistoryLimit; i < len(completed); i++ {
		bg := metav1.DeletePropagationBackground
		if err := r.Delete(ctx, &completed[i], &client.DeleteOptions{PropagationPolicy: &bg}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// listRunJobs lists Jobs managed by this NixJob.
func (r *NixJobReconciler) listRunJobs(ctx context.Context, nj *niov1alpha1.NixJob) ([]batchv1.Job, error) {
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(nj.Namespace), client.MatchingLabels{
		niov1alpha1.LabelWorkloadKind: kindNixJob,
		niov1alpha1.LabelWorkloadName: nj.Name,
	}); err != nil {
		return nil, err
	}
	return jobs.Items, nil
}

// SetupWithManager registers the NixJob controller with the manager.
func (r *NixJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := registerWorkloadIndexes(mgr, &niov1alpha1.NixJob{}, func(o client.Object) *niov1alpha1.NixSource {
		return &o.(*niov1alpha1.NixJob).Spec.Nix.Source
	}); err != nil {
		return err
	}
	b := ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.NixJob{}).
		Owns(&batchv1.Job{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			enqueueByIndex(r.Client, &niov1alpha1.NixJobList{}, IndexByCredentialsSecret))).
		Named("nixjob")
	addFluxSourceWatches(b, mgr, r.Client, &niov1alpha1.NixJobList{})
	return b.Complete(r)
}

// ensureBatchRestartPolicy sets a Job-compatible restartPolicy when the user
// left it empty (Jobs reject the default "Always").
func ensureBatchRestartPolicy(tmpl *corev1.PodTemplateSpec) {
	if tmpl.Spec.RestartPolicy == "" {
		tmpl.Spec.RestartPolicy = corev1.RestartPolicyNever
	}
}

// jobHasCondition reports whether the Job has the given condition set to True.
func jobHasCondition(job *batchv1.Job, condType batchv1.JobConditionType) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == condType && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
