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
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const kindNixStatefulSet = "NixStatefulSet"

// NixStatefulSetReconciler reconciles a NixStatefulSet into an owned apps/v1
// StatefulSet, stamping the resolved revision into the pod template. Ordering
// and PVCs are the StatefulSet controller's job.
type NixStatefulSetReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Git      GitResolver
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixstatefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixstatefulsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixstatefulsets/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the shared workload flow for NixStatefulSet.
func (r *NixStatefulSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var nss niov1alpha1.NixStatefulSet
	if err := r.Get(ctx, req.NamespacedName, &nss); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !nss.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, removeFinalizer(ctx, r.Client, &nss)
	}
	if err := ensureFinalizer(ctx, r.Client, &nss); err != nil {
		return ctrl.Result{}, err
	}

	st := &nss.Status.NixWorkloadStatus
	st.ObservedGeneration = nss.Generation
	setCondition(&st.Conditions, niov1alpha1.ConditionReconciling, metav1.ConditionTrue, reasonProgressing, "reconciling", nss.Generation)

	nix := nss.Spec.Nix
	if nix.Suspend {
		st.Phase = niov1alpha1.PhaseSuspended
		return ctrl.Result{}, r.Status().Update(ctx, &nss)
	}

	res, err := resolveRevision(ctx, r.Client, r.git(), nss.Namespace, nix.Source)
	if err != nil {
		setCondition(&st.Conditions, niov1alpha1.ConditionGitSynced, metav1.ConditionFalse, reasonGitError, err.Error(), nss.Generation)
		markStalled(st, reasonGitError, err.Error(), nss.Generation)
		_ = r.Status().Update(ctx, &nss)
		return ctrl.Result{RequeueAfter: infraRequeue}, nil
	}
	st.ResolvedRevision = res.revision
	st.LastPolledTime = &metav1.Time{Time: metav1.Now().Time}
	setCondition(&st.Conditions, niov1alpha1.ConditionGitSynced, metav1.ConditionTrue, reasonReady, "revision resolved", nss.Generation)

	deps, err := resolveInfra(ctx, r.Client, r.Scheme, &nss, nix)
	if err != nil {
		return ctrl.Result{}, err
	}
	if deps.notReady != "" {
		markStalled(st, reasonInfraNotReady, deps.notReady, nss.Generation)
		if uerr := r.Status().Update(ctx, &nss); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: infraRequeue}, nil
	}

	if err := r.project(ctx, &nss, res, deps); err != nil {
		return ctrl.Result{}, err
	}
	st.WorkloadRef = nss.Name

	if err := r.observe(ctx, &nss, res); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.Status().Update(ctx, &nss); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "failed to update NixStatefulSet status")
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: pollInterval(nix)}, nil
}

func (r *NixStatefulSetReconciler) git() GitResolver {
	if r.Git != nil {
		return r.Git
	}
	return ExecGitResolver{}
}

func (r *NixStatefulSetReconciler) project(ctx context.Context, nss *niov1alpha1.NixStatefulSet, res resolvedSource, deps infraDeps) error {
	desired := r.desiredStatefulSet(nss, res, deps)
	if err := controllerutil.SetControllerReference(nss, desired, r.Scheme); err != nil {
		return err
	}

	var existing appsv1.StatefulSet
	err := r.Get(ctx, client.ObjectKey{Namespace: nss.Namespace, Name: nss.Name}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	// Selector and volumeClaimTemplates are immutable; update the rest.
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template = desired.Spec.Template
	existing.Spec.UpdateStrategy = desired.Spec.UpdateStrategy
	return r.Update(ctx, &existing)
}

func (r *NixStatefulSetReconciler) desiredStatefulSet(nss *niov1alpha1.NixStatefulSet, res resolvedSource, deps infraDeps) *appsv1.StatefulSet {
	spec := *nss.Spec.StatefulSetTemplate.DeepCopy()

	in := renderInput{
		spec:             nss.Spec.Nix,
		resolvedRevision: res.revision,
		artifactURL:      res.artifactURL,
		store:            deps.store,
		builder:          deps.builder,
		sshSecretName:    deps.sshSecretName,
		kind:             kindNixStatefulSet,
		name:             nss.Name,
	}
	spec.Template = renderPodTemplate(in, spec.Template)

	if spec.Selector == nil {
		spec.Selector = &metav1.LabelSelector{MatchLabels: managedLabels(kindNixStatefulSet, nss.Name)}
	}
	// No maxUnavailable default: the ordered RollingUpdate already halts on the
	// first unready pod, keeping lower-ordinal old pods serving (design §3.3).

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nss.Name,
			Namespace: nss.Namespace,
			Labels:    managedLabels(kindNixStatefulSet, nss.Name),
		},
		Spec: spec,
	}
}

func (r *NixStatefulSetReconciler) observe(ctx context.Context, nss *niov1alpha1.NixStatefulSet, res resolvedSource) error {
	st := &nss.Status.NixWorkloadStatus
	rev := compositeRevision(res.revision, nss.Spec.Nix.Run, nss.Spec.Nix.Args)

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: nss.Namespace, Name: nss.Name}, &sts); err != nil {
		return err
	}
	nss.Status.ReadyReplicas = sts.Status.ReadyReplicas
	nss.Status.UpdatedReplicas = sts.Status.UpdatedReplicas
	nss.Status.CurrentRevision = sts.Status.CurrentRevision
	nss.Status.UpdateRevision = sts.Status.UpdateRevision

	desired := int32(1)
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}

	initState, err := observePodInit(ctx, r.Client, nss.Namespace, kindNixStatefulSet, nss.Name, rev)
	if err != nil {
		return err
	}
	if initState.failing {
		markStalled(st, reasonInitFailing, "new-revision pods are failing their instantiate init build", nss.Generation)
		return nil
	}

	rolledOut := sts.Status.ObservedGeneration >= sts.Generation &&
		sts.Status.UpdatedReplicas == desired &&
		sts.Status.ReadyReplicas == desired &&
		desired > 0

	switch {
	case rolledOut:
		markReady(st, res.revision, nss.Generation)
	case initState.building:
		markProgressing(st, niov1alpha1.PhaseBuilding, "new-revision pods are building in init", nss.Generation)
	default:
		markProgressing(st, niov1alpha1.PhaseProgressing, "statefulset rollout in progress", nss.Generation)
	}
	return nil
}

// SetupWithManager registers the NixStatefulSet controller with the manager.
func (r *NixStatefulSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := registerWorkloadIndexes(mgr, &niov1alpha1.NixStatefulSet{}, func(o client.Object) *niov1alpha1.NixSource {
		return &o.(*niov1alpha1.NixStatefulSet).Spec.Nix.Source
	}); err != nil {
		return err
	}
	b := ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.NixStatefulSet{}).
		Owns(&appsv1.StatefulSet{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			enqueueByIndex(r.Client, &niov1alpha1.NixStatefulSetList{}, IndexByCredentialsSecret))).
		Named("nixstatefulset")
	addFluxSourceWatches(b, mgr, r.Client, &niov1alpha1.NixStatefulSetList{})
	return b.Complete(r)
}
