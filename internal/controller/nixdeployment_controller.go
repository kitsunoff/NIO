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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const kindNixDeployment = "NixDeployment"

// NixDeploymentReconciler reconciles a NixDeployment into an owned apps/v1
// Deployment, stamping the resolved revision into the pod template.
type NixDeploymentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Git      GitResolver
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixstores;nixbuilders,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories;ocirepositories;buckets,verbs=get;list;watch

// Reconcile implements the shared workload flow for NixDeployment.
func (r *NixDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var nd niov1alpha1.NixDeployment
	if err := r.Get(ctx, req.NamespacedName, &nd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion: drop the finalizer; the owned Deployment is GC'd by ownerRef.
	if !nd.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, removeFinalizer(ctx, r.Client, &nd)
	}
	if err := ensureFinalizer(ctx, r.Client, &nd); err != nil {
		return ctrl.Result{}, err
	}

	st := &nd.Status.NixWorkloadStatus
	st.ObservedGeneration = nd.Generation
	setCondition(&st.Conditions, niov1alpha1.ConditionReconciling, metav1.ConditionTrue, reasonProgressing, "reconciling", nd.Generation)

	nix := nd.Spec.Nix
	if nix.Suspend {
		st.Phase = niov1alpha1.PhaseSuspended
		return ctrl.Result{}, r.Status().Update(ctx, &nd)
	}

	// 1. Resolve revision.
	res, err := resolveRevision(ctx, r.Client, r.git(), nd.Namespace, nix.Source)
	if err != nil {
		setCondition(&st.Conditions, niov1alpha1.ConditionGitSynced, metav1.ConditionFalse, reasonGitError, err.Error(), nd.Generation)
		markStalled(st, reasonGitError, err.Error(), nd.Generation)
		_ = r.Status().Update(ctx, &nd)
		return ctrl.Result{RequeueAfter: infraRequeue}, nil
	}
	st.ResolvedRevision = res.revision
	st.LastPolledTime = &metav1.Time{Time: metav1.Now().Time}
	setCondition(&st.Conditions, niov1alpha1.ConditionGitSynced, metav1.ConditionTrue, reasonReady, "revision resolved", nd.Generation)

	// 2. Infra preflight — do not advance the rollout if referenced infra is down.
	deps, err := resolveInfra(ctx, r.Client, r.Scheme, &nd, nix)
	if err != nil {
		return ctrl.Result{}, err
	}
	if deps.notReady != "" {
		markStalled(st, reasonInfraNotReady, deps.notReady, nd.Generation)
		if uerr := r.Status().Update(ctx, &nd); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: infraRequeue}, nil
	}

	// 3. Project the owned Deployment.
	if err := r.project(ctx, &nd, res, deps); err != nil {
		return ctrl.Result{}, err
	}
	st.WorkloadRef = nd.Name

	// 4. Observe.
	if err := r.observe(ctx, &nd, res); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.Status().Update(ctx, &nd); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "failed to update NixDeployment status")
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: pollInterval(nix)}, nil
}

func (r *NixDeploymentReconciler) git() GitResolver {
	if r.Git != nil {
		return r.Git
	}
	return ExecGitResolver{}
}

// project builds and applies the owned Deployment with the rendered pod template.
func (r *NixDeploymentReconciler) project(ctx context.Context, nd *niov1alpha1.NixDeployment, res resolvedSource, deps infraDeps) error {
	desired := r.desiredDeployment(nd, res, deps)
	if err := controllerutil.SetControllerReference(nd, desired, r.Scheme); err != nil {
		return err
	}

	var existing appsv1.Deployment
	err := r.Get(ctx, client.ObjectKey{Namespace: nd.Namespace, Name: nd.Name}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	// Selector is immutable; update the mutable spec fields only.
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template = desired.Spec.Template
	existing.Spec.Strategy = desired.Spec.Strategy
	return r.Update(ctx, &existing)
}

func (r *NixDeploymentReconciler) desiredDeployment(nd *niov1alpha1.NixDeployment, res resolvedSource, deps infraDeps) *appsv1.Deployment {
	var spec appsv1.DeploymentSpec
	if nd.Spec.DeploymentTemplate != nil {
		spec = *nd.Spec.DeploymentTemplate.DeepCopy()
	}

	in := renderInput{
		spec:             nd.Spec.Nix,
		resolvedRevision: res.revision,
		artifactURL:      res.artifactURL,
		store:            deps.store,
		builder:          deps.builder,
		sshSecretName:    deps.sshSecretName,
		kind:             kindNixDeployment,
		name:             nd.Name,
	}
	spec.Template = renderPodTemplate(in, spec.Template)

	// Stamp the managed selector when the user omitted one (immutable afterward).
	if spec.Selector == nil {
		spec.Selector = &metav1.LabelSelector{MatchLabels: managedLabels(kindNixDeployment, nd.Name)}
	}

	// Surge-only default so a broken revision stalls without shedding capacity
	// from the healthy old revision (design §3.3).
	if spec.Strategy.Type == "" && spec.Strategy.RollingUpdate == nil {
		zero := intstr.FromInt(0)
		surge := intstr.FromString("25%")
		spec.Strategy = appsv1.DeploymentStrategy{
			Type:          appsv1.RollingUpdateDeploymentStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &zero, MaxSurge: &surge},
		}
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nd.Name,
			Namespace: nd.Namespace,
			Labels:    managedLabels(kindNixDeployment, nd.Name),
		},
		Spec: spec,
	}
}

// observe reads the owned Deployment and new-revision pods to set phase/status.
func (r *NixDeploymentReconciler) observe(ctx context.Context, nd *niov1alpha1.NixDeployment, res resolvedSource) error {
	st := &nd.Status.NixWorkloadStatus
	rev := compositeRevision(res.revision, nd.Spec.Nix.Run, nd.Spec.Nix.Args)

	var dep appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: nd.Namespace, Name: nd.Name}, &dep); err != nil {
		return err
	}
	nd.Status.ReadyReplicas = dep.Status.ReadyReplicas
	nd.Status.UpdatedReplicas = dep.Status.UpdatedReplicas
	nd.Status.AvailableReplicas = dep.Status.AvailableReplicas

	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}

	// New-revision pods failing their init build stall the rollout (§2.1).
	initState, err := observePodInit(ctx, r.Client, nd.Namespace, kindNixDeployment, nd.Name, rev)
	if err != nil {
		return err
	}
	if initState.failing {
		markStalled(st, reasonInitFailing, "new-revision pods are failing their instantiate init build", nd.Generation)
		return nil
	}

	// Native ReplicaFailure => Degraded/Stalled.
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue {
			markStalled(st, reasonReplicaFailure, c.Message, nd.Generation)
			return nil
		}
	}

	rolledOut := dep.Status.ObservedGeneration >= dep.Generation &&
		dep.Status.UpdatedReplicas == desired &&
		dep.Status.AvailableReplicas == desired &&
		dep.Status.ReadyReplicas == desired &&
		desired > 0

	switch {
	case rolledOut:
		markReady(st, res.revision, nd.Generation)
	case initState.building:
		markProgressing(st, niov1alpha1.PhaseBuilding, "new-revision pods are building in init", nd.Generation)
	default:
		markProgressing(st, niov1alpha1.PhaseProgressing, "deployment rollout in progress", nd.Generation)
	}
	return nil
}

// SetupWithManager registers the NixDeployment controller with the manager.
func (r *NixDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := registerWorkloadIndexes(mgr, &niov1alpha1.NixDeployment{}, func(o client.Object) *niov1alpha1.NixSource {
		return &o.(*niov1alpha1.NixDeployment).Spec.Nix.Source
	}); err != nil {
		return err
	}
	b := ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.NixDeployment{}).
		Owns(&appsv1.Deployment{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			enqueueByIndex(r.Client, &niov1alpha1.NixDeploymentList{}, IndexByCredentialsSecret))).
		Named("nixdeployment")
	addFluxSourceWatches(b, mgr, r.Client, &niov1alpha1.NixDeploymentList{})
	return b.Complete(r)
}
