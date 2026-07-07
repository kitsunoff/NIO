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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const (
	// defaultPollInterval is used when NixSource.PollInterval is unset.
	defaultPollInterval = time.Minute

	// infraRequeue is the short requeue used while referenced infra is not ready.
	infraRequeue = 15 * time.Second

	// reasonInfraNotReady marks a Stalled condition due to store/builder.
	reasonInfraNotReady  = "InfraNotReady"
	reasonGitError       = "GitError"
	reasonProgressing    = "Progressing"
	reasonReady          = "Ready"
	reasonReplicaFailure = "ReplicaFailure"
	reasonInitFailing    = "InitBuildFailing"
)

// infraDeps carries the resolved store/builder wiring for pod rendering, plus a
// non-empty notReady reason when referenced infrastructure exists but is not
// Ready (in which case the rollout must not advance — design §7).
type infraDeps struct {
	store    *storeInfo
	builder  *builderInfo
	notReady string
	// sshSecretName is the store-owned SSH keypair Secret the runner pods mount
	// to dispatch builds to the builder (set only when a builder is in play).
	sshSecretName string
}

// resolveInfra resolves the workload's storeRef/builderRef/builderTemplate into
// the endpoints pod rendering needs. A builderTemplate materializes a dedicated
// NixBuilder owned by the workload. When a referenced NixStore/NixBuilder is not
// Ready, notReady is set and the caller stalls the rollout.
func resolveInfra(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, nix niov1alpha1.NixSpec) (infraDeps, error) {
	var deps infraDeps
	ns := owner.GetNamespace()

	if nix.StoreRef != nil {
		var store niov1alpha1.NixStore
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: nix.StoreRef.Name}, &store); err != nil {
			if apierrors.IsNotFound(err) {
				deps.notReady = fmt.Sprintf("NixStore %q not found", nix.StoreRef.Name)
				return deps, nil
			}
			return deps, err
		}
		if store.Status.Phase != niov1alpha1.PhaseReady || store.Status.SubstituterURL == "" {
			deps.notReady = fmt.Sprintf("NixStore %q not ready", nix.StoreRef.Name)
			return deps, nil
		}
		deps.store = &storeInfo{
			substituterURL: store.Status.SubstituterURL,
			publicKey:      store.Status.PublicKey,
			pushURL:        fmt.Sprintf("ssh-ng://root@%s.%s.svc", store.Name, ns),
		}
	}

	// Determine the builder to use: an explicit ref, or a dedicated one built
	// from a template (owned by the workload).
	builderName := ""
	switch {
	case nix.BuilderRef != nil:
		builderName = nix.BuilderRef.Name
	case nix.BuilderTemplate != nil:
		name, err := ensureOwnedBuilder(ctx, c, scheme, owner, nix)
		if err != nil {
			return deps, err
		}
		builderName = name
	}

	if builderName != "" {
		var builder niov1alpha1.NixBuilder
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: builderName}, &builder); err != nil {
			if apierrors.IsNotFound(err) {
				deps.notReady = fmt.Sprintf("NixBuilder %q not found", builderName)
				return deps, nil
			}
			return deps, err
		}
		if !builder.Status.Ready || builder.Status.BuilderEndpoint == "" {
			deps.notReady = fmt.Sprintf("NixBuilder %q not ready", builderName)
			return deps, nil
		}
		deps.builder = &builderInfo{endpoint: builder.Status.BuilderEndpoint, systems: builder.Spec.Systems}

		// Runner pods dispatch builds to the builder using the SSH key owned by the
		// store the builder pushes into (its storeRef, else the workload's storeRef).
		storeForSSH := builder.Spec.StoreRef
		if storeForSSH == nil {
			storeForSSH = nix.StoreRef
		}
		if storeForSSH != nil {
			deps.sshSecretName = sshSecretName(storeForSSH.Name)
		}
	}

	return deps, nil
}

// ensureOwnedBuilder creates (once) a dedicated NixBuilder owned by the workload
// from nix.BuilderTemplate, defaulting its StoreRef to the workload's StoreRef.
func ensureOwnedBuilder(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, nix niov1alpha1.NixSpec) (string, error) {
	name := owner.GetName() + "-builder"
	var existing niov1alpha1.NixBuilder
	err := c.Get(ctx, client.ObjectKey{Namespace: owner.GetNamespace(), Name: name}, &existing)
	if err == nil {
		return name, nil
	}
	if !apierrors.IsNotFound(err) {
		return "", err
	}
	spec := *nix.BuilderTemplate.DeepCopy()
	if spec.StoreRef == nil {
		spec.StoreRef = nix.StoreRef
	}
	builder := &niov1alpha1.NixBuilder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.GetNamespace()},
		Spec:       spec,
	}
	if err := controllerutil.SetControllerReference(owner, builder, scheme); err != nil {
		return "", err
	}
	if err := c.Create(ctx, builder); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", err
	}
	return name, nil
}

// pollInterval returns the reconcile requeue cadence for a workload.
func pollInterval(nix niov1alpha1.NixSpec) time.Duration {
	if nix.Source.PollInterval != nil && nix.Source.PollInterval.Duration > 0 {
		return nix.Source.PollInterval.Duration
	}
	return defaultPollInterval
}

// triggerOnChange returns the effective TriggerOnChange for a workload, applying
// the per-kind default when unset.
func triggerOnChange(nix niov1alpha1.NixSpec, defaultVal bool) bool {
	if nix.TriggerOnChange != nil {
		return *nix.TriggerOnChange
	}
	return defaultVal
}

// setCondition is a thin wrapper over meta.SetStatusCondition.
func setCondition(conds *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, msg string, gen int64) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type: condType, Status: status, Reason: reason, Message: msg, ObservedGeneration: gen,
	})
}

// markStalled sets Stalled=True, Ready=False, and the Degraded phase.
func markStalled(st *niov1alpha1.NixWorkloadStatus, reason, msg string, gen int64) {
	st.Phase = niov1alpha1.PhaseDegraded
	setCondition(&st.Conditions, niov1alpha1.ConditionStalled, metav1.ConditionTrue, reason, msg, gen)
	setCondition(&st.Conditions, niov1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg, gen)
}

// clearStalled removes the Stalled condition (progress resumed).
func clearStalled(st *niov1alpha1.NixWorkloadStatus) {
	meta.RemoveStatusCondition(&st.Conditions, niov1alpha1.ConditionStalled)
}

// markReady sets the Ready phase and conditions and records the rolled-out rev.
func markReady(st *niov1alpha1.NixWorkloadStatus, rolledOut string, gen int64) {
	st.Phase = niov1alpha1.PhaseReady
	st.RolledOutRevision = rolledOut
	setCondition(&st.Conditions, niov1alpha1.ConditionReady, metav1.ConditionTrue, reasonReady, "workload is ready", gen)
	setCondition(&st.Conditions, niov1alpha1.ConditionProgressing, metav1.ConditionFalse, reasonReady, "rollout complete", gen)
	clearStalled(st)
}

// markProgressing sets the Progressing phase/condition (rollout advancing).
func markProgressing(st *niov1alpha1.NixWorkloadStatus, phase, msg string, gen int64) {
	st.Phase = phase
	setCondition(&st.Conditions, niov1alpha1.ConditionProgressing, metav1.ConditionTrue, reasonProgressing, msg, gen)
	setCondition(&st.Conditions, niov1alpha1.ConditionReady, metav1.ConditionFalse, reasonProgressing, msg, gen)
}

// podInitState summarizes new-revision pods' init-container health for a rollout.
type podInitState struct {
	building bool // a new-revision pod is still running its init-containers
	failing  bool // a new-revision pod's instantiate init has failed
}

// observePodInit inspects pods carrying the target revision label and reports
// whether their init build is still running or has failed (design §2.1, §5).
func observePodInit(ctx context.Context, c client.Client, ns, kind, name, revision string) (podInitState, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{
		niov1alpha1.LabelWorkloadKind: kind,
		niov1alpha1.LabelWorkloadName: name,
		niov1alpha1.LabelRevision:     revision,
	}); err != nil {
		return podInitState{}, err
	}
	var state podInitState
	for i := range pods.Items {
		for _, ics := range pods.Items[i].Status.InitContainerStatuses {
			if ics.Name != initInstantiate {
				continue
			}
			if t := ics.State.Terminated; t != nil && t.ExitCode != 0 {
				state.failing = true
			}
			if w := ics.State.Waiting; w != nil && (w.Reason == "CrashLoopBackOff" || w.Reason == "Error") {
				state.failing = true
			}
			if ics.State.Running != nil || (ics.State.Waiting != nil && !state.failing) {
				state.building = true
			}
		}
	}
	return state, nil
}

// ensureFinalizer adds the workload finalizer if missing, persisting the change.
func ensureFinalizer(ctx context.Context, c client.Client, obj client.Object) error {
	if controllerutil.ContainsFinalizer(obj, niov1alpha1.WorkloadFinalizer) {
		return nil
	}
	controllerutil.AddFinalizer(obj, niov1alpha1.WorkloadFinalizer)
	return c.Update(ctx, obj)
}

// removeFinalizer drops the workload finalizer on deletion. The owned native
// workload is garbage-collected by its ownerReference.
func removeFinalizer(ctx context.Context, c client.Client, obj client.Object) error {
	if !controllerutil.ContainsFinalizer(obj, niov1alpha1.WorkloadFinalizer) {
		return nil
	}
	controllerutil.RemoveFinalizer(obj, niov1alpha1.WorkloadFinalizer)
	return c.Update(ctx, obj)
}
