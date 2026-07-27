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
	"path"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const (
	// clusterNodesDir is where per-member node files are injected in the flake
	// repo (import-tree auto-imports them).
	clusterNodesDir = "modules/nodes"

	// convergeChildSuffix names the one owned converge NixCronJob.
	convergeChildSuffix = "-converge"

	clusterSSHVolumeName = "nio-cluster-ssh"
	clusterSSHMountPath  = "/etc/nio/cluster-ssh"
	clusterSSHKeyPath    = clusterSSHMountPath + "/ssh-privatekey"

	clusterAgeVolumeName = "nio-cluster-age"
	clusterAgeMountPath  = "/etc/nio/age"
	clusterAgeKeyPath    = clusterAgeMountPath + "/keys.txt"

	// clusterNixSSHOpts is permissive by decision (no host-key pinning in the
	// MVP) — matches the single-host orchestrator's posture.
	clusterNixSSHOpts = "-i " + clusterSSHKeyPath +
		" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"

	// convergeActiveDeadlineSeconds caps one converge run (generous — a real run
	// builds and SSHes to every member).
	convergeActiveDeadlineSeconds = int64(3600)

	// defaultClusterDayTwoSchedule is the fallback converge cadence.
	defaultClusterDayTwoSchedule = "*/30 * * * *"

	// clusterRequeue is the steady-state requeue interval.
	clusterRequeue = 30 * time.Second
)

func convergeCronName(cluster string) string      { return cluster + convergeChildSuffix }
func clusterAppInstallable(cluster string) string { return ".#cluster-" + cluster }

// NixClusterReconciler reconciles a NixCluster object: it selects Machines per
// nodeGroup (stable + sticky), renders per-member node files, and drives ONE
// idempotent converge NixCronJob. NIO stays abstract — converge (in the
// downstream flake repo) owns ordering, install/switch, secrets, and post-ops.
type NixClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixcronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixbuilders,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives a NixCluster toward its desired state.
func (r *NixClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cluster niov1alpha1.NixCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !cluster.DeletionTimestamp.IsZero() {
		return r.reconcileRemoving(ctx, &cluster)
	}

	if !controllerutil.ContainsFinalizer(&cluster, niov1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(&cluster, niov1alpha1.FinalizerName)
		if err := r.Update(ctx, &cluster); err != nil {
			return ctrl.Result{}, err
		}
	}

	// List every Machine in the cluster's namespace (selectors are label-based).
	var machineList niov1alpha1.MachineList
	if err := r.List(ctx, &machineList, client.InNamespace(cluster.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	byName := make(map[string]*niov1alpha1.Machine, len(machineList.Items))
	for i := range machineList.Items {
		byName[machineList.Items[i].Name] = &machineList.Items[i]
	}

	// Previous per-group members (for sticky selection) and their last reported
	// status (the honest answer when a converge never ran).
	prev := make(map[string][]string, len(cluster.Status.NodeGroups))
	prevStatus := make(map[string]string)
	for _, g := range cluster.Status.NodeGroups {
		names := make([]string, 0, len(g.Members))
		for _, m := range g.Members {
			names = append(names, m.Name)
			prevStatus[m.Name] = m.Status
		}
		prev[g.Name] = names
	}

	claimed := make(map[string]bool)
	groupStatuses := make([]niov1alpha1.NodeGroupStatus, 0, len(cluster.Spec.NodeGroups))
	var nodeFiles []niov1alpha1.NixFile
	anyUnder := false
	totalMembers := 0

	for i := range cluster.Spec.NodeGroups {
		group := &cluster.Spec.NodeGroups[i]
		sel, err := metav1.LabelSelectorAsSelector(&group.Selector)
		if err != nil {
			return r.fail(ctx, &cluster, niov1alpha1.NixClusterPhaseBlocked, niov1alpha1.ReasonSelectionComplete,
				fmt.Errorf("group %q selector: %w", group.Name, err))
		}
		candidates := matchingMachines(machineList.Items, sel, claimed)
		members, under := selectGroupMembers(candidates, prev[group.Name], group.Count)
		for _, name := range members {
			claimed[name] = true
		}
		if under {
			anyUnder = true
		}

		gs := niov1alpha1.NodeGroupStatus{Name: group.Name, Selected: int32(len(members))}
		if group.Count != nil {
			gs.Desired = *group.Count
		} else {
			gs.Desired = int32(len(candidates))
		}

		for _, name := range members {
			m := byName[name]
			nf, err := renderMemberNodeFile(cluster.Name, name, m.Spec.Host, group.Values)
			if err != nil {
				return r.fail(ctx, &cluster, niov1alpha1.NixClusterPhaseBlocked, niov1alpha1.ReasonInvalidNodeFile, err)
			}
			nodeFiles = append(nodeFiles, nf)
			gs.Members = append(gs.Members, niov1alpha1.MemberStatus{Name: name})
		}
		totalMembers += len(members)
		groupStatuses = append(groupStatuses, gs)
	}

	if err := validateAdditionalFiles(nodeFiles); err != nil {
		return r.fail(ctx, &cluster, niov1alpha1.NixClusterPhaseBlocked, niov1alpha1.ReasonInvalidNodeFile, err)
	}

	// Delegating builds sets max-jobs = 0, so a builder that cannot produce a
	// member's system fails the converge with an opaque nix error an hour later.
	// Refuse up front when the mismatch is provable.
	if err := r.checkBuilderCoversMembers(ctx, &cluster, byName, groupStatuses); err != nil {
		return r.fail(ctx, &cluster, niov1alpha1.NixClusterPhaseBlocked, reasonBuilderSystemMismatch, err)
	}

	if err := r.ensureConvergeCronJob(ctx, &cluster, nodeFiles); err != nil {
		return r.fail(ctx, &cluster, niov1alpha1.NixClusterPhaseDegraded, niov1alpha1.ReasonFailed, err)
	}

	// Observe the converge cron for a coarse, job-level per-node status and phase
	// (parsing per-member JSON is best-effort and deferred; O11 in the design).
	var cron niov1alpha1.NixCronJob
	haveCron := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: convergeCronName(cluster.Name)}, &cron) == nil
	for gi := range groupStatuses {
		for mi := range groupStatuses[gi].Members {
			member := &groupStatuses[gi].Members[mi]
			member.Status = coarseMemberStatus(&cron, haveCron, prevStatus[member.Name])
		}
	}

	cluster.Status.NodeGroups = groupStatuses
	cluster.Status.ConvergeJobRef = convergeCronName(cluster.Name)
	cluster.Status.Phase = clusterPhase(&cron, haveCron, totalMembers)

	r.setConditions(&cluster, &cron, haveCron, anyUnder, totalMembers)

	// Advance ObservedGeneration only on a successful reconcile, so a
	// failing/stalled reconcile (which returns early via fail()) does not
	// prematurely mark this generation observed.
	cluster.Status.ObservedGeneration = cluster.Generation

	if err := r.Status().Update(ctx, &cluster); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "failed to update NixCluster status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: clusterRequeue}, nil
}

// matchingMachines returns the names of Machines matching sel that are not
// already claimed by an earlier nodeGroup, sorted ascending (deterministic).
func matchingMachines(machines []niov1alpha1.Machine, sel labels.Selector, claimed map[string]bool) []string {
	var names []string
	for i := range machines {
		m := &machines[i]
		if claimed[m.Name] {
			continue
		}
		if sel.Matches(labels.Set(m.Labels)) {
			names = append(names, m.Name)
		}
	}
	sort.Strings(names)
	return names
}

// selectGroupMembers implements the stable + sticky selection (design
// "Selection algorithm"). candidates must be sorted ascending and already
// claim-filtered; prev is the group's previously-selected members.
//
// count unset  → all candidates are members.
// count set    → keep still-matching prev members (stable), then top up. Top-up
//
//	grows UPWARD from the current max member first (so a newly-added lower name
//	never pre-empts an existing member — S1 step 6 expects the vacancy to be
//	filled by the next name above the current members, not the globally-lowest),
//	then falls back to lowest-first if upward candidates are exhausted. Over
//	count (Count reduced) → drop the highest-name extras.
//
// The returned members are sorted ascending. underprovisioned is true when
// fewer than count members could be selected.
func selectGroupMembers(candidates, prev []string, count *int32) (members []string, underprovisioned bool) {
	if count == nil {
		return append([]string(nil), candidates...), false
	}
	n := int(*count)
	if n < 0 {
		n = 0
	}

	candSet := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		candSet[c] = true
	}

	kept := make([]string, 0, n)
	keptSet := make(map[string]bool)
	for _, p := range prev {
		if candSet[p] && !keptSet[p] {
			kept = append(kept, p)
			keptSet[p] = true
		}
	}

	// Over count: drop the highest-name extras.
	if len(kept) > n {
		sort.Strings(kept)
		for _, k := range kept[n:] {
			delete(keptSet, k)
		}
		kept = append([]string(nil), kept[:n]...)
	}

	if len(kept) < n {
		maxKept := ""
		for _, k := range kept {
			if k > maxKept {
				maxKept = k
			}
		}
		// First pass: grow upward from the current max member.
		for _, c := range candidates {
			if len(kept) >= n {
				break
			}
			if !keptSet[c] && c > maxKept {
				kept = append(kept, c)
				keptSet[c] = true
			}
		}
		// Fallback: lowest-first for any remaining slots (robustness).
		for _, c := range candidates {
			if len(kept) >= n {
				break
			}
			if !keptSet[c] {
				kept = append(kept, c)
				keptSet[c] = true
			}
		}
	}

	sort.Strings(kept)
	return kept, len(kept) < n
}

// renderMemberNodeFile builds the per-member node file for the flake repo:
//
//	nixcluster.<cluster>.members.<machine> =
//	  recursiveUpdate (fromJSON <values>) { install.ip = <host>; };
//
// It deliberately omits nixosConfiguration so the member inherits the
// cluster-level default. The Machine name is validated (single relative path
// segment; reused validateFilePath charset/traversal rules) so a hostile name
// cannot escape modules/nodes/ or inject shell/nix metacharacters.
func renderMemberNodeFile(clusterName, machineName, host string, values *apiextensionsv1.JSON) (niov1alpha1.NixFile, error) {
	if machineName == "" || strings.ContainsAny(machineName, "/") || strings.Contains(machineName, "..") {
		return niov1alpha1.NixFile{}, fmt.Errorf("member %q: name must be a single safe path segment", machineName)
	}
	nodePath := path.Join(clusterNodesDir, machineName+".nix")
	if err := validateFilePath(nodePath); err != nil {
		return niov1alpha1.NixFile{}, fmt.Errorf("member %q: %w", machineName, err)
	}
	valuesJSON := "{}"
	if values != nil && len(values.Raw) > 0 {
		valuesJSON = string(values.Raw)
	}
	content := renderNodeFileContent(clusterName, machineName, host, valuesJSON)
	return niov1alpha1.NixFile{Path: nodePath, Inline: &content}, nil
}

// renderNodeFileContent formats the flake-parts module for one member. The
// values JSON and the Machine host are both interpolated into double-quoted Nix
// strings via escapeNixString so neither can break out of the string or inject
// a live antiquotation.
func renderNodeFileContent(clusterName, machineName, host, valuesJSON string) string {
	return fmt.Sprintf(`{ lib, ... }:
{
  nixcluster.%q.members.%q =
    lib.recursiveUpdate (builtins.fromJSON "%s") {
      install.ip = "%s";
    };
}
`, clusterName, machineName, escapeNixString(valuesJSON), escapeNixString(host))
}

// escapeNixString escapes a Go string for safe interpolation into a Nix
// double-quoted string literal. Order matters: backslash MUST be escaped first
// so the backslashes introduced for " and ${ are not themselves doubled. This
// neutralises string-breakout (") and live antiquotations (${...}); it is used
// for BOTH the values JSON and the Machine host.
func escapeNixString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`) // backslash first
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `${`, `\${`)
	return s
}

// convergePodTemplate returns the converge pod template with the cluster SSH key
// and age key mounted, and NIX_SSHOPTS/SOPS_AGE_KEY_FILE set. nixrender
// preserves these (upsert) on the app container.
func convergePodTemplate(cluster *niov1alpha1.NixCluster) corev1.PodTemplateSpec {
	mode := int32(0o400)
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	var env []corev1.EnvVar

	if cluster.Spec.SSHKeyRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: clusterSSHVolumeName,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: cluster.Spec.SSHKeyRef.Name, DefaultMode: &mode,
			}},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name: clusterSSHVolumeName, MountPath: clusterSSHMountPath, ReadOnly: true,
		})
		env = append(env, corev1.EnvVar{Name: "NIX_SSHOPTS", Value: clusterNixSSHOpts})
	}
	if cluster.Spec.AgeKeyRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: clusterAgeVolumeName,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: cluster.Spec.AgeKeyRef.Name, DefaultMode: &mode,
			}},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name: clusterAgeVolumeName, MountPath: clusterAgeMountPath, ReadOnly: true,
		})
		env = append(env, corev1.EnvVar{Name: "SOPS_AGE_KEY_FILE", Value: clusterAgeKeyPath})
	}

	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Volumes: volumes,
			Containers: []corev1.Container{{
				Name:         defaultAppContainer,
				Env:          env,
				VolumeMounts: mounts,
			}},
		},
	}
}

// desiredConvergeCronJob builds the one owned converge NixCronJob.
func desiredConvergeCronJob(cluster *niov1alpha1.NixCluster, files []niov1alpha1.NixFile) *niov1alpha1.NixCronJob {
	schedule := cluster.Spec.DayTwoSchedule
	if schedule == "" {
		schedule = defaultClusterDayTwoSchedule
	}
	deadline := convergeActiveDeadlineSeconds
	return &niov1alpha1.NixCronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      convergeCronName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    managedLabels("NixCluster", cluster.Name),
		},
		Spec: niov1alpha1.NixCronJobSpec{
			Nix: niov1alpha1.NixSpec{
				Source:          cluster.Spec.Source,
				Run:             clusterAppInstallable(cluster.Name),
				Args:            []string{"converge"},
				AdditionalFiles: files,
				TriggerOnChange: ptr(true),
				// Optional store/builder acceleration: when the NixCluster
				// references a NixStore/NixBuilder, the converge pod builds
				// against the shared store instead of rebuilding the member
				// closure in an ephemeral in-pod /nix on every run.
				StoreRef:   cluster.Spec.StoreRef,
				BuilderRef: cluster.Spec.BuilderRef,
			},
			CronJobTemplate: batchv1.CronJobSpec{
				Schedule:          schedule,
				ConcurrencyPolicy: batchv1.ForbidConcurrent,
				JobTemplate: batchv1.JobTemplateSpec{
					Spec: batchv1.JobSpec{
						ActiveDeadlineSeconds: &deadline,
						Template:              convergePodTemplate(cluster),
					},
				},
			},
		},
	}
}

// ensureConvergeCronJob creates or updates the owned converge NixCronJob.
func (r *NixClusterReconciler) ensureConvergeCronJob(
	ctx context.Context, cluster *niov1alpha1.NixCluster, files []niov1alpha1.NixFile,
) error {
	desired := desiredConvergeCronJob(cluster, files)
	cron := &niov1alpha1.NixCronJob{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cron, func() error {
		cron.Labels = desired.Labels
		cron.Spec = desired.Spec
		return controllerutil.SetControllerReference(cluster, cron, r.Scheme)
	})
	return err
}

// nixSystemForArch maps a Machine's reported architecture onto the nix system
// string a NixBuilder advertises. Only the two architectures NIO actually
// supports are mapped: anything else returns "" and proves nothing, so the
// preflight stays silent rather than guessing.
func nixSystemForArch(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "x86_64", "amd64":
		return "x86_64-linux"
	case "aarch64", "arm64":
		return "aarch64-linux"
	default:
		return ""
	}
}

// checkBuilderCoversMembers refuses a cluster whose builderRef provably cannot
// build a selected member's architecture. Deliberately conservative — it only
// reports a mismatch when every input is known:
//
//   - no builderRef, or the builder cannot be read: nothing to check here (the
//     child stalls with InfraNotReady and the cluster mirrors that);
//   - a NixBuilder with no spec.systems advertises both common Linux arches, so
//     no claim is falsifiable;
//   - a Machine that has not reported its architecture yet is skipped.
func (r *NixClusterReconciler) checkBuilderCoversMembers(
	ctx context.Context,
	cluster *niov1alpha1.NixCluster,
	byName map[string]*niov1alpha1.Machine,
	groups []niov1alpha1.NodeGroupStatus,
) error {
	if cluster.Spec.BuilderRef == nil {
		return nil
	}
	var builder niov1alpha1.NixBuilder
	key := client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Spec.BuilderRef.Name}
	if err := r.Get(ctx, key, &builder); err != nil {
		return nil
	}
	if len(builder.Spec.Systems) == 0 {
		return nil
	}
	supported := make(map[string]bool, len(builder.Spec.Systems))
	for _, system := range builder.Spec.Systems {
		supported[system] = true
	}

	var mismatches []string
	for gi := range groups {
		for _, member := range groups[gi].Members {
			machine := byName[member.Name]
			if machine == nil || machine.Status.HardwareFacts == nil {
				continue
			}
			system := nixSystemForArch(machine.Status.HardwareFacts.Architecture)
			if system == "" || supported[system] {
				continue
			}
			mismatches = append(mismatches, fmt.Sprintf("%s needs %s", member.Name, system))
		}
	}
	if len(mismatches) == 0 {
		return nil
	}
	return fmt.Errorf("NixBuilder %q builds only [%s], but %s; converge delegates every build (max-jobs = 0) so there is no local fallback",
		builder.Name, strings.Join(builder.Spec.Systems, " "), strings.Join(mismatches, ", "))
}

// convergeStall returns the converge cron's Stalled condition when it is stalled
// on an unresolvable NixStore/NixBuilder reference. Deliberately narrow: the
// child also stalls on git errors, which are a different failure with a
// different meaning, and the clearing side (isMirroredStall) must recognise
// exactly the same set of conditions this returns.
func convergeStall(cron *niov1alpha1.NixCronJob, haveCron bool) *metav1.Condition {
	if !haveCron {
		return nil
	}
	cond := meta.FindStatusCondition(cron.Status.Conditions, niov1alpha1.ConditionStalled)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonInfraNotReady {
		return nil
	}
	return cond
}

// coarseMemberStatus derives a job-level per-node status from the converge cron.
// prevStatus is what this member reported on the previous reconcile, used when
// the current converge state says nothing new about the member.
func coarseMemberStatus(cron *niov1alpha1.NixCronJob, haveCron bool, prevStatus string) string {
	if !haveCron {
		return niov1alpha1.MemberStatusPending
	}
	// A stalled converge applied nothing, so the member's status is unchanged —
	// literally whatever it was before, not a guess derived from the cron.
	if convergeStall(cron, haveCron) != nil {
		if prevStatus != "" {
			return prevStatus
		}
		return niov1alpha1.MemberStatusPending
	}
	// A cron whose most recent finished run failed did try to apply, and failed.
	if failed, _ := latestRunFailed(cron.Status.LastFailedTime, cron.Status.LastSuccessfulTime); failed {
		return niov1alpha1.MemberStatusFailed
	}
	switch cron.Status.Phase {
	case niov1alpha1.PhaseFailed, niov1alpha1.PhaseDegraded:
		return niov1alpha1.MemberStatusFailed
	case niov1alpha1.PhaseReady:
		return niov1alpha1.MemberStatusApplied
	}
	if len(cron.Status.ActiveJobs) > 0 {
		return niov1alpha1.MemberStatusApplying
	}
	if cron.Status.LastSuccessfulTime != nil {
		return niov1alpha1.MemberStatusApplied
	}
	return niov1alpha1.MemberStatusPending
}

// clusterPhase maps the converge cron state (and selection) to a coarse phase.
func clusterPhase(cron *niov1alpha1.NixCronJob, haveCron bool, totalMembers int) string {
	if totalMembers == 0 {
		return niov1alpha1.NixClusterPhaseBlocked
	}
	if !haveCron {
		return niov1alpha1.NixClusterPhaseConverging
	}
	// Waiting on a dependency we cannot resolve is Blocked; Degraded would claim
	// the cluster itself is broken.
	if convergeStall(cron, haveCron) != nil {
		return niov1alpha1.NixClusterPhaseBlocked
	}
	switch cron.Status.Phase {
	case niov1alpha1.PhaseFailed, niov1alpha1.PhaseDegraded:
		return niov1alpha1.NixClusterPhaseDegraded
	case niov1alpha1.PhaseReady:
		return niov1alpha1.NixClusterPhaseReady
	}
	if cron.Status.LastSuccessfulTime != nil {
		return niov1alpha1.NixClusterPhaseReady
	}
	return niov1alpha1.NixClusterPhaseConverging
}

// setConditions sets Ready, Stalled, GitSynced, and Underprovisioned.
func (r *NixClusterReconciler) setConditions(
	cluster *niov1alpha1.NixCluster, cron *niov1alpha1.NixCronJob, haveCron, anyUnder bool, totalMembers int,
) {
	gen := cluster.Generation

	if anyUnder {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionUnderprovisioned, Status: metav1.ConditionTrue,
			Reason: niov1alpha1.ReasonUnderprovisioned, ObservedGeneration: gen,
			Message: "one or more nodeGroups have fewer matching Machines than requested",
		})
	} else {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionUnderprovisioned, Status: metav1.ConditionFalse,
			Reason: niov1alpha1.ReasonFullyProvisioned, ObservedGeneration: gen,
			Message: "all nodeGroups satisfied their requested count",
		})
	}

	gitSynced := haveCron && cron.Status.ResolvedRevision != ""
	gitStatus := metav1.ConditionFalse
	gitReason := niov1alpha1.ReasonWaiting
	gitMsg := "converge has not resolved a revision yet"
	if gitSynced {
		gitStatus = metav1.ConditionTrue
		gitReason = niov1alpha1.ReasonSucceeded
		gitMsg = "converge resolved revision " + cron.Status.ResolvedRevision
	}
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionGitSynced, Status: gitStatus,
		Reason: gitReason, ObservedGeneration: gen, Message: gitMsg,
	})

	// The converge child holds the only accurate diagnosis when it is stalled on
	// an unresolvable storeRef/builderRef — mirror it onto the cluster so
	// `kubectl describe` names the broken reference.
	stall := convergeStall(cron, haveCron)

	ready := cluster.Status.Phase == niov1alpha1.NixClusterPhaseReady
	if ready {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionReady, Status: metav1.ConditionTrue,
			Reason: niov1alpha1.ReasonSucceeded, ObservedGeneration: gen,
			Message: "cluster converged",
		})
		meta.RemoveStatusCondition(&cluster.Status.Conditions, niov1alpha1.ConditionStalled)
	} else {
		reason := niov1alpha1.ReasonConverging
		msg := "converge in progress"
		if totalMembers == 0 {
			reason = niov1alpha1.ReasonWaiting
			msg = "no Machines selected for any nodeGroup"
		}
		if stall != nil {
			reason = stall.Reason
			msg = "converge is stalled: " + stall.Message
		}
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionReady, Status: metav1.ConditionFalse,
			Reason: reason, ObservedGeneration: gen, Message: msg,
		})
	}

	switch {
	case stall != nil:
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionStalled, Status: metav1.ConditionTrue,
			Reason: stall.Reason, ObservedGeneration: gen,
			Message: "converge is stalled: " + stall.Message,
		})
	case isMirroredStall(cluster):
		// The dependency resolved; drop the condition we mirrored earlier so the
		// cluster does not stay marked stalled forever. Stalled conditions set by
		// the reconciler itself (`fail`) carry other reasons and are left alone.
		meta.RemoveStatusCondition(&cluster.Status.Conditions, niov1alpha1.ConditionStalled)
	}
}

// isMirroredStall reports whether the cluster's Stalled condition is one we
// mirrored from the converge child rather than one the reconciler set itself.
func isMirroredStall(cluster *niov1alpha1.NixCluster) bool {
	cond := meta.FindStatusCondition(cluster.Status.Conditions, niov1alpha1.ConditionStalled)
	return cond != nil && cond.Reason == reasonInfraNotReady
}

// fail records a Degraded/Blocked + Stalled status and returns the error.
func (r *NixClusterReconciler) fail(
	ctx context.Context, cluster *niov1alpha1.NixCluster, phase, reason string, cause error,
) (ctrl.Result, error) {
	cluster.Status.Phase = phase
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionStalled, Status: metav1.ConditionTrue,
		Reason: reason, Message: cause.Error(), ObservedGeneration: cluster.Generation,
	})
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionReady, Status: metav1.ConditionFalse,
		Reason: reason, Message: cause.Error(), ObservedGeneration: cluster.Generation,
	})
	if err := r.Status().Update(ctx, cluster); err != nil && !apierrors.IsConflict(err) {
		logf.FromContext(ctx).Error(err, "failed to update NixCluster status after error")
	}
	return ctrl.Result{}, cause
}

// reconcileRemoving deletes the converge cron (owner GC also covers it) and
// clears the finalizer. Node teardown / decommission is deferred (design "Out").
func (r *NixClusterReconciler) reconcileRemoving(ctx context.Context, cluster *niov1alpha1.NixCluster) (ctrl.Result, error) {
	cron := &niov1alpha1.NixCronJob{
		ObjectMeta: metav1.ObjectMeta{Name: convergeCronName(cluster.Name), Namespace: cluster.Namespace},
	}
	if err := r.Delete(ctx, cron); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if controllerutil.ContainsFinalizer(cluster, niov1alpha1.FinalizerName) {
		controllerutil.RemoveFinalizer(cluster, niov1alpha1.FinalizerName)
		if err := r.Update(ctx, cluster); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// findClustersForMachine enqueues every NixCluster in the changed Machine's
// namespace (any could select it — selectors are label-based, not indexable).
func (r *NixClusterReconciler) findClustersForMachine(ctx context.Context, obj client.Object) []reconcile.Request {
	var list niov1alpha1.NixClusterList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKey{Namespace: list.Items[i].Namespace, Name: list.Items[i].Name},
		})
	}
	return requests
}

// SetupWithManager registers the NixCluster controller with the manager.
func (r *NixClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.NixCluster{}).
		Owns(&niov1alpha1.NixCronJob{}).
		Watches(
			&niov1alpha1.Machine{},
			handler.EnqueueRequestsFromMapFunc(r.findClustersForMachine),
		).
		Named("nixcluster").
		Complete(r)
}
