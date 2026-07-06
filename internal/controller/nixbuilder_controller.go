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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

const (
	// builderStoreVolumeName is the volume backing the builder's own /nix.
	builderStoreVolumeName = "nix-store"

	// nixBuilderRequeue is the steady-state requeue interval for a NixBuilder.
	nixBuilderRequeue = 30 * time.Second
)

// builderStartScript builds the shell for the builder container: an sshd that
// accepts remote builds, and — when a store is referenced — a nix post-build-hook
// that pushes every build result into the shared NixStore over ssh-ng, so other
// pods substitute it (ADR-0008). The build itself runs via `nix daemon --stdio`
// started by sshd per remote-build connection.
func builderStartScript(storeHost string) string {
	nixConfExtra := ""
	pushSetup := ""
	if storeHost != "" {
		nixConfExtra = "\npost-build-hook = /etc/nio/upload.sh"
		pushSetup = fmt.Sprintf(`
cat > /etc/nio/upload.sh <<'HOOK'
#!/bin/sh
set -eu
[ -n "${OUT_PATHS:-}" ] || exit 0
exec nix copy --to "ssh-ng://root@%s" $OUT_PATHS
HOOK
chmod +x /etc/nio/upload.sh
cp %s/%s /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
cat > /root/.ssh/config <<'SSHCFG'
Host *
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  IdentityFile %s
SSHCFG
chmod 600 /root/.ssh/config
`, storeHost, sshKeyMountPath, sshSecretPublicKey, sshPrivateKeyPath)
	}
	return fmt.Sprintf(`set -eu
mkdir -p /etc/nix /etc/nio /root/.ssh /run/sshd
chmod 700 /root/.ssh
cat > /etc/nix/nix.conf <<'NIXCFG'
experimental-features = nix-command flakes
trusted-users = root%s
NIXCFG
%s
exec nix shell nixpkgs#openssh --command sh -c 'ssh-keygen -A; $(command -v sshd) -D -e -o PermitRootLogin=prohibit-password -o PasswordAuthentication=no -o UsePAM=no'
`, nixConfExtra, pushSetup)
}

// NixBuilderReconciler reconciles a NixBuilder object: it manages a single
// builder-worker StatefulSet and a headless Service, then publishes the
// remote-build endpoint to status.
type NixBuilderReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixbuilders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixbuilders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixbuilders/finalizers,verbs=update

// Reconcile drives a NixBuilder toward its desired state.
func (r *NixBuilderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var builder niov1alpha1.NixBuilder
	if err := r.Get(ctx, req.NamespacedName, &builder); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	builder.Status.ObservedGeneration = builder.Generation

	if err := r.ensureService(ctx, &builder); err != nil {
		return r.fail(ctx, &builder, "ServiceError", err)
	}
	if err := r.ensureStatefulSet(ctx, &builder); err != nil {
		return r.fail(ctx, &builder, "StatefulSetError", err)
	}

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: builder.Namespace, Name: builder.Name}, &sts); err != nil {
		return r.fail(ctx, &builder, "StatefulSetError", err)
	}

	builder.Status.BuilderEndpoint = fmt.Sprintf("ssh-ng://root@%s.%s.svc", builder.Name, builder.Namespace)
	builder.Status.Ready = sts.Status.ReadyReplicas >= 1

	if builder.Status.Ready {
		builder.Status.Phase = niov1alpha1.PhaseReady
		meta.SetStatusCondition(&builder.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionReady, Status: metav1.ConditionTrue,
			Reason: "BuilderReady", Message: "builder worker is ready",
			ObservedGeneration: builder.Generation,
		})
		meta.RemoveStatusCondition(&builder.Status.Conditions, niov1alpha1.ConditionStalled)
	} else {
		builder.Status.Phase = niov1alpha1.PhasePending
		meta.SetStatusCondition(&builder.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionReady, Status: metav1.ConditionFalse,
			Reason: "BuilderNotReady", Message: "builder worker is not ready",
			ObservedGeneration: builder.Generation,
		})
	}

	if err := r.Status().Update(ctx, &builder); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "failed to update NixBuilder status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: nixBuilderRequeue}, nil
}

func (r *NixBuilderReconciler) fail(ctx context.Context, builder *niov1alpha1.NixBuilder, reason string, cause error) (ctrl.Result, error) {
	builder.Status.Phase = niov1alpha1.PhaseDegraded
	builder.Status.Ready = false
	meta.SetStatusCondition(&builder.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionStalled, Status: metav1.ConditionTrue,
		Reason: reason, Message: cause.Error(), ObservedGeneration: builder.Generation,
	})
	meta.SetStatusCondition(&builder.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionReady, Status: metav1.ConditionFalse,
		Reason: reason, Message: cause.Error(), ObservedGeneration: builder.Generation,
	})
	if err := r.Status().Update(ctx, builder); err != nil && !apierrors.IsConflict(err) {
		logf.FromContext(ctx).Error(err, "failed to update NixBuilder status after error")
	}
	return ctrl.Result{}, cause
}

func (r *NixBuilderReconciler) ensureService(ctx context.Context, builder *niov1alpha1.NixBuilder) error {
	labels := managedLabels("NixBuilder", builder.Name)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: builder.Name, Namespace: builder.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if svc.Labels == nil {
			svc.Labels = map[string]string{}
		}
		for k, v := range labels {
			svc.Labels[k] = v
		}
		svc.Spec.ClusterIP = corev1.ClusterIPNone
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "ssh", Port: int32(NixBuilderSSHPort), TargetPort: intstr.FromInt(NixBuilderSSHPort)},
		}
		return controllerutil.SetControllerReference(builder, svc, r.Scheme)
	})
	return err
}

func (r *NixBuilderReconciler) ensureStatefulSet(ctx context.Context, builder *niov1alpha1.NixBuilder) error {
	desired := r.desiredStatefulSet(builder)
	if err := controllerutil.SetControllerReference(builder, desired, r.Scheme); err != nil {
		return err
	}

	var existing appsv1.StatefulSet
	err := r.Get(ctx, client.ObjectKey{Namespace: builder.Namespace, Name: builder.Name}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	existing.Spec.Template = desired.Spec.Template
	return r.Update(ctx, &existing)
}

func (r *NixBuilderReconciler) desiredStatefulSet(builder *niov1alpha1.NixBuilder) *appsv1.StatefulSet {
	labels := managedLabels("NixBuilder", builder.Name)
	image := builder.Spec.Image
	if image == "" {
		image = DefaultNixBuilderImage
	}
	replicas := int32(1) // single worker, deliberately

	podSpec := corev1.PodSpec{}
	if builder.Spec.Template != nil {
		podSpec = *builder.Spec.Template.Spec.DeepCopy()
	}

	env := []corev1.EnvVar{
		{Name: "NIX_CONFIG", Value: "experimental-features = nix-command flakes"},
	}
	if builder.Spec.MaxJobs != nil {
		env = append(env, corev1.EnvVar{Name: "NIX_MAX_JOBS", Value: fmt.Sprintf("%d", *builder.Spec.MaxJobs)})
	}

	workerMounts := []corev1.VolumeMount{{Name: builderStoreVolumeName, MountPath: "/nix"}}
	storeHost := ""
	if sr := builder.Spec.StoreRef; sr != nil {
		storeHost = fmt.Sprintf("%s.%s.svc", sr.Name, builder.Namespace)
		workerMounts = append(workerMounts, corev1.VolumeMount{Name: sshVolumeName, MountPath: sshKeyMountPath, ReadOnly: true})
		podSpec.Volumes = upsertVolume(podSpec.Volumes, corev1.Volume{
			Name: sshVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: sshSecretName(sr.Name)},
			},
		})
	}

	worker := corev1.Container{
		Name:  "builder",
		Image: image,
		// Accept remote builds over sshd; when a store is referenced, a
		// post-build-hook pushes every build result into the shared NixStore over
		// ssh-ng so other pods substitute it (ADR-0008).
		Command:      []string{"sh", "-c", builderStartScript(storeHost)},
		Ports:        []corev1.ContainerPort{{Name: "ssh", ContainerPort: int32(NixBuilderSSHPort)}},
		Env:          env,
		VolumeMounts: workerMounts,
	}
	// bootstrap seeds nix into the volume-backed /nix so the daemon binary is not
	// shadowed by the empty volume (mirrors the workload/store pattern).
	bootstrap := corev1.Container{
		Name:         "bootstrap",
		Image:        image,
		Command:      []string{"sh", "-c", "[ -e /nix-vol/store ] || cp --archive /nix/. /nix-vol/"},
		VolumeMounts: []corev1.VolumeMount{{Name: builderStoreVolumeName, MountPath: "/nix-vol"}},
	}
	podSpec.InitContainers = append([]corev1.Container{bootstrap},
		filterOutContainers(podSpec.InitContainers, "bootstrap")...)
	podSpec.Containers = upsertContainer(podSpec.Containers, worker)

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: builder.Name, Namespace: builder.Namespace, Labels: labels},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: builder.Name,
			Replicas:    &replicas,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}

	// The builder's own /nix: a persistent volumeClaimTemplate when Storage is
	// set (so the build cache survives restarts), otherwise a pod-local emptyDir.
	if builder.Spec.Storage != nil {
		sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{Name: builderStoreVolumeName},
				Spec:       *builder.Spec.Storage,
			},
		}
	} else {
		sts.Spec.Template.Spec.Volumes = upsertVolume(sts.Spec.Template.Spec.Volumes, corev1.Volume{
			Name:         builderStoreVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}

	return sts
}

// SetupWithManager registers the NixBuilder controller with the manager.
func (r *NixBuilderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.NixBuilder{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Named("nixbuilder").
		Complete(r)
}
