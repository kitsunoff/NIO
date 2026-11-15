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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const (
	// teardownChildSuffix names the one owned teardown NixJob.
	teardownChildSuffix = "-teardown"

	// teardownBackoffBase is the wait after the first failed teardown run; it
	// doubles with every further recorded failure up to teardownBackoffMax.
	teardownBackoffBase = 30 * time.Second
	teardownBackoffMax  = 10 * time.Minute

	// teardownPollInterval is the requeue while waiting for converge to stop or
	// for the teardown run to finish.
	teardownPollInterval = 15 * time.Second

	// clusterResultMarker prefixes the single machine-readable result line the
	// cluster CLI prints on stdout (converge and teardown alike).
	clusterResultMarker = "::nixcluster:result::"

	// clusterResultPartial is the overall result of a run that skipped members
	// (unreachable hosts) without failing any.
	clusterResultPartial = "partial"

	// memberResultSkipped and memberResultFailed are per-member statuses in the
	// result line.
	memberResultSkipped = "Skipped"
	memberResultFailed  = "Failed"

	// teardownLogTailLines bounds how much of the run's log is read; the result
	// line is printed at the very end of the run.
	teardownLogTailLines = int64(200)

	kindCronJob = "CronJob"
)

func teardownJobName(cluster string) string { return cluster + teardownChildSuffix }

// teardownArgs are the cluster app arguments of a teardown run. No `--json`:
// the pod log merges stdout and stderr, and the marker is what makes the result
// line findable there (the converge child is started the same way).
func teardownArgs() []string { return []string{"teardown", "--auto-approve"} }

// JobLogReader returns the app container log of the newest pod of a batch Job.
type JobLogReader interface {
	JobLog(ctx context.Context, namespace, jobName string) (string, error)
}

// ClientsetJobLogReader reads pod logs through an uncached clientset, so the
// operator does not keep an informer over every pod in the cluster.
type ClientsetJobLogReader struct {
	Clientset kubernetes.Interface
}

// JobLog implements JobLogReader.
func (r ClientsetJobLogReader) JobLog(ctx context.Context, namespace, jobName string) (string, error) {
	pods, err := r.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: batchv1.JobNameLabel + "=" + jobName,
	})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pod found for Job %s", jobName)
	}
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].CreationTimestamp.After(pods.Items[j].CreationTimestamp.Time)
	})
	tail := teardownLogTailLines
	raw, err := r.Clientset.CoreV1().Pods(namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{
		Container: defaultAppContainer,
		TailLines: &tail,
	}).DoRaw(ctx)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// clusterRunResult is the payload of the cluster CLI's result line.
type clusterRunResult struct {
	Cluster string             `json:"cluster"`
	Result  string             `json:"result"`
	Members []clusterRunMember `json:"members"`
}

// clusterRunMember is one member entry of the result line.
type clusterRunMember struct {
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// parseClusterResult finds the last result line in a run's log. The log merges
// stdout and stderr, so it scans for the `::nixcluster:result::` marker and, as
// a fallback, for the bare JSON object `--json` prints. A candidate counts only
// when it parses and names both the cluster and the result.
func parseClusterResult(log string) (*clusterRunResult, bool) {
	lines := strings.Split(log, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		var payload string
		switch idx := strings.Index(line, clusterResultMarker); {
		case idx >= 0:
			payload = strings.TrimSpace(line[idx+len(clusterResultMarker):])
		case strings.HasPrefix(line, "{"):
			payload = line
		default:
			continue
		}
		var res clusterRunResult
		if err := json.Unmarshal([]byte(payload), &res); err != nil || res.Cluster == "" || res.Result == "" {
			continue
		}
		return &res, true
	}
	return nil, false
}

// summarize renders a result for a condition or event message: the overall
// result, counts per member status, and the message of every member that did
// not apply.
func (res *clusterRunResult) summarize() string {
	counts := map[string]int{}
	details := make([]string, 0, len(res.Members))
	for _, m := range res.Members {
		counts[m.Status]++
		if m.Status != memberResultSkipped && m.Status != memberResultFailed {
			continue
		}
		detail := m.Name + " " + strings.ToLower(m.Status)
		if m.Message != "" {
			detail += ": " + m.Message
		}
		details = append(details, detail)
	}
	statuses := make([]string, 0, len(counts))
	for status, n := range counts {
		statuses = append(statuses, fmt.Sprintf("%d %s", n, status))
	}
	sort.Strings(statuses)
	msg := "result " + res.Result
	if len(statuses) > 0 {
		msg += " (" + strings.Join(statuses, ", ") + ")"
	}
	if len(details) > 0 {
		msg += "; " + strings.Join(details, "; ")
	}
	return msg
}

// skippedMembers lists the members the run skipped.
func (res *clusterRunResult) skippedMembers() []string {
	var names []string
	for _, m := range res.Members {
		if m.Status == memberResultSkipped {
			names = append(names, m.Name)
		}
	}
	return names
}

// teardownBackoff is the wait before the run after the given number of
// recorded failures: teardownBackoffBase doubled per failure after the first,
// capped at teardownBackoffMax.
func teardownBackoff(failed int32) time.Duration {
	d := teardownBackoffBase
	for i := int32(1); i < failed; i++ {
		d *= 2
		if d >= teardownBackoffMax {
			return teardownBackoffMax
		}
	}
	return d
}

// desiredTeardownJob builds the teardown NixJob from the converge child, so the
// run reaches exactly the members the last converge reached: same source, node
// files, store/builder and pod template (SSH and age keys). Only the arguments
// change. It runs once (no trigger on a new revision) and without pod retries —
// the cluster controller owns retries.
func desiredTeardownJob(cluster *niov1alpha1.NixCluster, cron *niov1alpha1.NixCronJob) *niov1alpha1.NixJob {
	nix := *cron.Spec.Nix.DeepCopy()
	nix.Run = clusterAppInstallable(cluster.Name)
	nix.Args = teardownArgs()
	nix.Suspend = false
	nix.TriggerOnChange = ptr(false)

	jobSpec := cron.Spec.CronJobTemplate.JobTemplate.Spec.DeepCopy()
	jobSpec.BackoffLimit = ptr(int32(0))

	return &niov1alpha1.NixJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      teardownJobName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    managedLabels("NixCluster", cluster.Name),
		},
		Spec: niov1alpha1.NixJobSpec{Nix: nix, JobTemplate: jobSpec},
	}
}

// reconcileRemoving runs the deletion policy. Retain releases the cluster at
// once; Teardown holds the finalizer until a teardown run has succeeded.
func (r *NixClusterReconciler) reconcileRemoving(ctx context.Context, cluster *niov1alpha1.NixCluster) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cluster, niov1alpha1.FinalizerName) {
		return ctrl.Result{}, nil
	}
	if cluster.Spec.DeletionPolicy == niov1alpha1.DeletionPolicyRetain {
		return ctrl.Result{}, r.releaseCluster(ctx, cluster)
	}
	return r.reconcileTeardown(ctx, cluster)
}

// releaseCluster deletes the converge child and removes the finalizer. Owner
// garbage collection removes a teardown NixJob that may still exist.
func (r *NixClusterReconciler) releaseCluster(ctx context.Context, cluster *niov1alpha1.NixCluster) error {
	cron := &niov1alpha1.NixCronJob{
		ObjectMeta: metav1.ObjectMeta{Name: convergeCronName(cluster.Name), Namespace: cluster.Namespace},
	}
	if err := r.Delete(ctx, cron); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	controllerutil.RemoveFinalizer(cluster, niov1alpha1.FinalizerName)
	return r.Update(ctx, cluster)
}

// reconcileTeardown drives one step of the teardown state machine:
//
//	teardown NixJob exists → observe it (running, succeeded, failed);
//	no NixJob, no converge child → nothing NIO converged, release;
//	no NixJob → suspend converge, wait for it, then create the NixJob.
func (r *NixClusterReconciler) reconcileTeardown(ctx context.Context, cluster *niov1alpha1.NixCluster) (ctrl.Result, error) {
	var job niov1alpha1.NixJob
	err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: teardownJobName(cluster.Name)}, &job)
	switch {
	case err == nil:
		return r.observeTeardownJob(ctx, cluster, &job)
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}

	var cron niov1alpha1.NixCronJob
	err = r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: convergeCronName(cluster.Name)}, &cron)
	if apierrors.IsNotFound(err) {
		r.event(cluster, corev1.EventTypeNormal, "TeardownSkipped",
			"no converge child exists, so there is nothing to tear down")
		return ctrl.Result{}, r.releaseCluster(ctx, cluster)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.suspendConvergeForTeardown(ctx, &cron); err != nil {
		return ctrl.Result{}, err
	}
	busy, why, err := r.convergeStillRunning(ctx, &cron)
	if err != nil {
		return ctrl.Result{}, err
	}
	if busy {
		markTearingDown(cluster, niov1alpha1.ReasonWaitingForConverge, why)
		return r.updateTeardownStatus(ctx, cluster, teardownPollInterval)
	}

	desired := desiredTeardownJob(cluster, &cron)
	if err := controllerutil.SetControllerReference(cluster, desired, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	td := teardownStatus(cluster)
	td.Attempts++
	td.JobRef = desired.Name
	msg := fmt.Sprintf("teardown run %s started (attempt %d)", desired.Name, td.Attempts)
	r.event(cluster, corev1.EventTypeNormal, "TeardownStarted", msg)
	markTearingDown(cluster, niov1alpha1.ReasonTeardownRunning, msg)
	return r.updateTeardownStatus(ctx, cluster, teardownPollInterval)
}

// observeTeardownJob maps the teardown NixJob's state onto the cluster.
func (r *NixClusterReconciler) observeTeardownJob(
	ctx context.Context, cluster *niov1alpha1.NixCluster, job *niov1alpha1.NixJob,
) (ctrl.Result, error) {
	// A NixJob with this name that the cluster does not control is not ours to
	// observe or delete; holding the finalizer is the only safe answer.
	if !metav1.IsControlledBy(job, cluster) {
		markTearingDown(cluster, niov1alpha1.ReasonJobNotOwned, fmt.Sprintf(
			"NixJob %s exists but is not controlled by this cluster; remove it or set deletionPolicy: Retain", job.Name))
		return r.updateTeardownStatus(ctx, cluster, teardownPollInterval)
	}

	td := teardownStatus(cluster)
	td.JobRef = job.Name
	// The NixJob exists, so at least one attempt was made even if the status
	// write after its creation was lost.
	if td.Attempts == 0 {
		td.Attempts = 1
	}

	if !job.DeletionTimestamp.IsZero() {
		markTearingDown(cluster, niov1alpha1.ReasonRetryScheduled, "waiting for the failed teardown run to be removed")
		return r.updateTeardownStatus(ctx, cluster, teardownPollInterval)
	}

	switch job.Status.Phase {
	case niov1alpha1.PhaseReady:
		return r.finishTeardown(ctx, cluster, job)
	case niov1alpha1.PhaseFailed:
		return r.handleTeardownFailure(ctx, cluster, job)
	}

	msg := fmt.Sprintf("teardown run %s in progress (attempt %d)", job.Name, td.Attempts)
	if stall := meta.FindStatusCondition(job.Status.Conditions, niov1alpha1.ConditionStalled); stall != nil &&
		stall.Status == metav1.ConditionTrue {
		msg = fmt.Sprintf("teardown run %s is stalled: %s", job.Name, stall.Message)
	}
	markTearingDown(cluster, niov1alpha1.ReasonTeardownRunning, msg)
	return r.updateTeardownStatus(ctx, cluster, teardownPollInterval)
}

// finishTeardown records a successful run, deletes the converge child and
// removes the finalizer. The run's exit status is the authority: `partial`
// (unreachable members skipped) exits zero and releases the cluster too.
func (r *NixClusterReconciler) finishTeardown(
	ctx context.Context, cluster *niov1alpha1.NixCluster, job *niov1alpha1.NixJob,
) (ctrl.Result, error) {
	res, summary := r.readTeardownResult(ctx, cluster, job)
	recordTeardownResult(cluster, res, "teardown succeeded: "+summary)

	if res != nil && res.Result == clusterResultPartial {
		if skipped := res.skippedMembers(); len(skipped) > 0 {
			r.event(cluster, corev1.EventTypeWarning, "TeardownPartial", fmt.Sprintf(
				"teardown skipped %s; the cluster state on those members is left as it is",
				strings.Join(skipped, ", ")))
		}
	}
	r.event(cluster, corev1.EventTypeNormal, "TeardownSucceeded", "teardown succeeded: "+summary)

	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionTearingDown, Status: metav1.ConditionFalse,
		Reason: niov1alpha1.ReasonSucceeded, ObservedGeneration: cluster.Generation,
		Message: "teardown succeeded: " + summary,
	})
	meta.RemoveStatusCondition(&cluster.Status.Conditions, niov1alpha1.ConditionTeardownFailed)
	if err := r.Status().Update(ctx, cluster); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.releaseCluster(ctx, cluster)
}

// handleTeardownFailure records a failed run once, keeps the finalizer, and
// replaces the failed NixJob when the backoff has elapsed. The failed NixJob is
// kept during the backoff so its pod log stays readable.
func (r *NixClusterReconciler) handleTeardownFailure(
	ctx context.Context, cluster *niov1alpha1.NixCluster, job *niov1alpha1.NixJob,
) (ctrl.Result, error) {
	td := teardownStatus(cluster)
	if td.FailedAttempts < td.Attempts || td.LastFailureTime == nil {
		res, summary := r.readTeardownResult(ctx, cluster, job)
		msg := fmt.Sprintf("teardown run %s failed (attempt %d): %s", job.Name, td.Attempts, summary)
		recordTeardownResult(cluster, res, msg)
		td.FailedAttempts = td.Attempts
		td.LastFailureTime = &metav1.Time{Time: time.Now()}
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionTeardownFailed, Status: metav1.ConditionTrue,
			Reason: niov1alpha1.ReasonTeardownFailed, ObservedGeneration: cluster.Generation,
			Message: msg,
		})
		r.event(cluster, corev1.EventTypeWarning, niov1alpha1.ReasonTeardownFailed, msg)
	}

	wait := teardownBackoff(td.FailedAttempts) - time.Since(td.LastFailureTime.Time)
	if wait > 0 {
		markTearingDown(cluster, niov1alpha1.ReasonRetryScheduled, fmt.Sprintf(
			"teardown failed %d time(s); next run in %s", td.FailedAttempts, wait.Round(time.Second)))
		return r.updateTeardownStatus(ctx, cluster, wait)
	}

	background := metav1.DeletePropagationBackground
	if err := r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &background}); err != nil &&
		!apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	markTearingDown(cluster, niov1alpha1.ReasonRetryScheduled, "replacing the failed teardown run")
	return r.updateTeardownStatus(ctx, cluster, teardownPollInterval)
}

// readTeardownResult reads and parses the result line of the run, returning
// the parsed result (nil when unavailable) and a message describing it.
func (r *NixClusterReconciler) readTeardownResult(
	ctx context.Context, cluster *niov1alpha1.NixCluster, job *niov1alpha1.NixJob,
) (*clusterRunResult, string) {
	runJob := job.Status.ActiveJob
	if runJob == "" {
		runJob = job.Status.WorkloadRef
	}
	if r.Logs == nil || runJob == "" {
		return nil, "result line unavailable"
	}
	log, err := r.Logs.JobLog(ctx, cluster.Namespace, runJob)
	if err != nil {
		return nil, "result line unavailable: " + err.Error()
	}
	res, ok := parseClusterResult(log)
	if !ok {
		return nil, "no result line in the log of Job " + runJob
	}
	return res, res.summarize()
}

// recordTeardownResult stores a finished run's result in status.teardown.
func recordTeardownResult(cluster *niov1alpha1.NixCluster, res *clusterRunResult, msg string) {
	td := teardownStatus(cluster)
	td.Message = msg
	td.Result = ""
	td.Members = nil
	if res == nil {
		return
	}
	td.Result = res.Result
	for _, m := range res.Members {
		td.Members = append(td.Members, niov1alpha1.TeardownMemberStatus{
			Name: m.Name, Status: m.Status, Message: m.Message,
		})
	}
}

// suspendConvergeForTeardown stops the converge child from starting runs. It
// sets the native CronJob suspension, which the NixCronJob controller projects,
// and turns trigger-on-change off, which would otherwise fire a run on a new
// revision. `nix.suspend` is cleared on purpose: it pauses NIO's reconciliation
// of the child, so the native suspension would never reach the CronJob.
func (r *NixClusterReconciler) suspendConvergeForTeardown(ctx context.Context, cron *niov1alpha1.NixCronJob) error {
	spec := &cron.Spec
	if spec.CronJobTemplate.Suspend != nil && *spec.CronJobTemplate.Suspend &&
		spec.Nix.TriggerOnChange != nil && !*spec.Nix.TriggerOnChange && !spec.Nix.Suspend {
		return nil
	}
	spec.CronJobTemplate.Suspend = ptr(true)
	spec.Nix.TriggerOnChange = ptr(false)
	spec.Nix.Suspend = false
	return r.Update(ctx, cron)
}

// convergeStillRunning reports whether converge may still touch the members:
// the projected CronJob has not picked up the suspension yet, or a Job it (or
// the NixCronJob, for trigger-on-change runs) controls has not finished. Jobs
// are read directly; the child's status.activeJobs lags by one reconcile.
func (r *NixClusterReconciler) convergeStillRunning(ctx context.Context, cron *niov1alpha1.NixCronJob) (bool, string, error) {
	var cj batchv1.CronJob
	err := r.Get(ctx, client.ObjectKey{Namespace: cron.Namespace, Name: cron.Name}, &cj)
	switch {
	case err == nil:
		if cj.Spec.Suspend == nil || !*cj.Spec.Suspend {
			return true, fmt.Sprintf("waiting for CronJob %s to be suspended", cj.Name), nil
		}
	case !apierrors.IsNotFound(err):
		return false, "", err
	}

	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(cron.Namespace)); err != nil {
		return false, "", err
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		owner := metav1.GetControllerOf(job)
		if owner == nil || owner.Name != cron.Name || (owner.Kind != kindCronJob && owner.Kind != kindNixCronJob) {
			continue
		}
		if jobHasCondition(job, batchv1.JobComplete) || jobHasCondition(job, batchv1.JobFailed) {
			continue
		}
		return true, fmt.Sprintf("waiting for converge run %s to finish", job.Name), nil
	}
	return false, "", nil
}

// teardownStatus returns status.teardown, creating it on first use.
func teardownStatus(cluster *niov1alpha1.NixCluster) *niov1alpha1.TeardownStatus {
	if cluster.Status.Teardown == nil {
		cluster.Status.Teardown = &niov1alpha1.TeardownStatus{}
	}
	return cluster.Status.Teardown
}

// markTearingDown sets the TearingDown phase and condition, and Ready false.
func markTearingDown(cluster *niov1alpha1.NixCluster, reason, msg string) {
	cluster.Status.Phase = niov1alpha1.NixClusterPhaseTearingDown
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionTearingDown, Status: metav1.ConditionTrue,
		Reason: reason, ObservedGeneration: cluster.Generation, Message: msg,
	})
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionReady, Status: metav1.ConditionFalse,
		Reason: niov1alpha1.ReasonTearingDown, ObservedGeneration: cluster.Generation, Message: msg,
	})
}

// updateTeardownStatus writes the status and requeues after the given delay;
// a conflict requeues at once.
func (r *NixClusterReconciler) updateTeardownStatus(
	ctx context.Context, cluster *niov1alpha1.NixCluster, after time.Duration,
) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, cluster); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

// event records an event when a recorder is configured.
func (r *NixClusterReconciler) event(cluster *niov1alpha1.NixCluster, eventType, reason, msg string) {
	if r.Recorder != nil {
		r.Recorder.Event(cluster, eventType, reason, msg)
	}
}
