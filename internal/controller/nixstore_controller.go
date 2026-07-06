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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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
	// SigningKeySecretPrivateField is the Secret data key holding the Nix
	// signing (secret) key in "name:base64" form.
	SigningKeySecretPrivateField = "nix-signing-key"

	// SigningKeySecretPublicField is the Secret data key holding the Nix trusted
	// public key in "name:base64" form.
	SigningKeySecretPublicField = "nix-public-key"

	// nixStoreVolumeName is the volumeClaimTemplate name backing /nix.
	nixStoreVolumeName = "nix-store"

	// signingKeyVolumeName mounts the signing-key secret into the server pod.
	signingKeyVolumeName = "signing-key"

	// nixStoreRequeue is the steady-state requeue interval for a NixStore.
	nixStoreRequeue = 30 * time.Second
)

// NixStoreReconciler reconciles a NixStore object: it manages a StatefulSet
// running a Nix binary-cache server, a headless Service, and a signing-key
// Secret (generated when absent), then publishes the substituter/store
// endpoints and public key to status.
type NixStoreReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixstores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixstores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=nixstores/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives a NixStore toward its desired state.
func (r *NixStoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var store niov1alpha1.NixStore
	if err := r.Get(ctx, req.NamespacedName, &store); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	store.Status.ObservedGeneration = store.Generation

	// 1. Ensure the signing-key Secret and learn the public key.
	publicKey, err := r.ensureSigningKey(ctx, &store)
	if err != nil {
		return r.fail(ctx, &store, "SigningKeyError", err)
	}

	// 2. Ensure the headless Service.
	if err := r.ensureService(ctx, &store); err != nil {
		return r.fail(ctx, &store, "ServiceError", err)
	}

	// 3. Ensure the store-server StatefulSet.
	if err := r.ensureStatefulSet(ctx, &store); err != nil {
		return r.fail(ctx, &store, "StatefulSetError", err)
	}

	// 4. Observe and publish status.
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: store.Name}, &sts); err != nil {
		return r.fail(ctx, &store, "StatefulSetError", err)
	}

	desired := int32(1)
	if store.Spec.Replicas != nil {
		desired = *store.Spec.Replicas
	}
	store.Status.ReadyReplicas = sts.Status.ReadyReplicas
	store.Status.PublicKey = publicKey
	store.Status.SubstituterURL = fmt.Sprintf("http://%s.%s.svc:%d", store.Name, store.Namespace, NixStoreHTTPPort)
	store.Status.StoreURI = fmt.Sprintf("ssh-ng://root@%s.%s.svc", store.Name, store.Namespace)

	if sts.Status.ReadyReplicas >= desired && desired > 0 {
		store.Status.Phase = "Ready"
		meta.SetStatusCondition(&store.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionReady, Status: metav1.ConditionTrue,
			Reason: "StoreReady", Message: "store server is ready",
			ObservedGeneration: store.Generation,
		})
		meta.RemoveStatusCondition(&store.Status.Conditions, niov1alpha1.ConditionStalled)
	} else {
		store.Status.Phase = "Pending"
		meta.SetStatusCondition(&store.Status.Conditions, metav1.Condition{
			Type: niov1alpha1.ConditionReady, Status: metav1.ConditionFalse,
			Reason: "StoreNotReady", Message: fmt.Sprintf("%d/%d server replicas ready", sts.Status.ReadyReplicas, desired),
			ObservedGeneration: store.Generation,
		})
	}

	if err := r.Status().Update(ctx, &store); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "failed to update NixStore status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: nixStoreRequeue}, nil
}

// fail records a Degraded/Stalled status and returns the error for requeue.
func (r *NixStoreReconciler) fail(ctx context.Context, store *niov1alpha1.NixStore, reason string, cause error) (ctrl.Result, error) {
	store.Status.Phase = "Degraded"
	meta.SetStatusCondition(&store.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionStalled, Status: metav1.ConditionTrue,
		Reason: reason, Message: cause.Error(), ObservedGeneration: store.Generation,
	})
	meta.SetStatusCondition(&store.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionReady, Status: metav1.ConditionFalse,
		Reason: reason, Message: cause.Error(), ObservedGeneration: store.Generation,
	})
	if err := r.Status().Update(ctx, store); err != nil && !apierrors.IsConflict(err) {
		logf.FromContext(ctx).Error(err, "failed to update NixStore status after error")
	}
	return ctrl.Result{}, cause
}

// ensureSigningKey ensures a signing-key Secret exists and returns the public
// key string. When SigningKeySecretRef is set, that Secret is read (and must
// carry the public key). Otherwise an owned Secret is generated once.
func (r *NixStoreReconciler) ensureSigningKey(ctx context.Context, store *niov1alpha1.NixStore) (string, error) {
	if store.Spec.SigningKeySecretRef != nil {
		var secret corev1.Secret
		key := client.ObjectKey{Namespace: store.Namespace, Name: store.Spec.SigningKeySecretRef.Name}
		if err := r.Get(ctx, key, &secret); err != nil {
			return "", fmt.Errorf("reading signing-key secret %q: %w", store.Spec.SigningKeySecretRef.Name, err)
		}
		pub, ok := secret.Data[SigningKeySecretPublicField]
		if !ok || len(pub) == 0 {
			return "", fmt.Errorf("signing-key secret %q missing %q", secret.Name, SigningKeySecretPublicField)
		}
		return string(pub), nil
	}

	secretName := store.Name + "-signing-key"
	var secret corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: secretName}, &secret)
	if err == nil {
		if pub, ok := secret.Data[SigningKeySecretPublicField]; ok && len(pub) > 0 {
			return string(pub), nil
		}
		return "", fmt.Errorf("generated signing-key secret %q missing %q", secretName, SigningKeySecretPublicField)
	}
	if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("getting signing-key secret: %w", err)
	}

	keyName := fmt.Sprintf("%s-%s-1", store.Namespace, store.Name)
	priv, pub, genErr := generateNixSigningKey(keyName)
	if genErr != nil {
		return "", genErr
	}
	secret = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: store.Namespace,
			Labels:    managedLabels("NixStore", store.Name),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			SigningKeySecretPrivateField: []byte(priv),
			SigningKeySecretPublicField:  []byte(pub),
		},
	}
	if err := controllerutil.SetControllerReference(store, &secret, r.Scheme); err != nil {
		return "", err
	}
	if err := r.Create(ctx, &secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Raced with another reconcile; re-read.
			if getErr := r.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: secretName}, &secret); getErr == nil {
				return string(secret.Data[SigningKeySecretPublicField]), nil
			}
		}
		return "", fmt.Errorf("creating signing-key secret: %w", err)
	}
	return pub, nil
}

// generateNixSigningKey creates an ed25519 keypair formatted as Nix binary-cache
// keys: "<name>:<base64(secret)>" and "<name>:<base64(public)>".
func generateNixSigningKey(keyName string) (secretKey, publicKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generating ed25519 key: %w", err)
	}
	secretKey = keyName + ":" + base64.StdEncoding.EncodeToString(priv)
	publicKey = keyName + ":" + base64.StdEncoding.EncodeToString(pub)
	return secretKey, publicKey, nil
}

// ensureService creates the headless Service fronting the store server.
func (r *NixStoreReconciler) ensureService(ctx context.Context, store *niov1alpha1.NixStore) error {
	labels := managedLabels("NixStore", store.Name)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: store.Name, Namespace: store.Namespace}}
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
			{Name: "http", Port: int32(NixStoreHTTPPort), TargetPort: intstr.FromInt(NixStoreHTTPPort)},
			{Name: "ssh", Port: int32(NixStoreSSHPort), TargetPort: intstr.FromInt(NixStoreSSHPort)},
		}
		return controllerutil.SetControllerReference(store, svc, r.Scheme)
	})
	return err
}

// ensureStatefulSet creates (once) the store-server StatefulSet, or updates its
// mutable fields (replicas, container image) on subsequent reconciles.
func (r *NixStoreReconciler) ensureStatefulSet(ctx context.Context, store *niov1alpha1.NixStore) error {
	desired := r.desiredStatefulSet(store)
	if err := controllerutil.SetControllerReference(store, desired, r.Scheme); err != nil {
		return err
	}

	var existing appsv1.StatefulSet
	err := r.Get(ctx, client.ObjectKey{Namespace: store.Namespace, Name: store.Name}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Update only mutable fields; volumeClaimTemplates/selector are immutable.
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template = desired.Spec.Template
	return r.Update(ctx, &existing)
}

func (r *NixStoreReconciler) desiredStatefulSet(store *niov1alpha1.NixStore) *appsv1.StatefulSet {
	labels := managedLabels("NixStore", store.Name)
	image := store.Spec.Image
	if image == "" {
		image = DefaultNixStoreImage
	}
	replicas := int32(1)
	if store.Spec.Replicas != nil {
		replicas = *store.Spec.Replicas
	}

	podSpec := corev1.PodSpec{}
	if store.Spec.Template != nil {
		podSpec = *store.Spec.Template.Spec.DeepCopy()
	}
	// Ensure the store server container is present and owned.
	serverContainer := corev1.Container{
		Name:  "store",
		Image: image,
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: int32(NixStoreHTTPPort)}},
		Env: []corev1.EnvVar{
			{Name: "SIGN_KEY_PATHS", Value: "/etc/nix/signing/" + SigningKeySecretPrivateField},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: nixStoreVolumeName, MountPath: "/nix"},
			{Name: signingKeyVolumeName, MountPath: "/etc/nix/signing", ReadOnly: true},
		},
	}
	podSpec.Containers = upsertContainer(podSpec.Containers, serverContainer)
	podSpec.Volumes = upsertVolume(podSpec.Volumes, corev1.Volume{
		Name: signingKeyVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: r.signingKeySecretName(store)},
		},
	})

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: store.Name, Namespace: store.Namespace, Labels: labels},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: store.Name,
			Replicas:    &replicas,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: nixStoreVolumeName},
					Spec:       store.Spec.Storage,
				},
			},
		},
	}
}

func (r *NixStoreReconciler) signingKeySecretName(store *niov1alpha1.NixStore) string {
	if store.Spec.SigningKeySecretRef != nil {
		return store.Spec.SigningKeySecretRef.Name
	}
	return store.Name + "-signing-key"
}

// upsertContainer replaces a container with the same name, or appends it.
func upsertContainer(containers []corev1.Container, c corev1.Container) []corev1.Container {
	for i := range containers {
		if containers[i].Name == c.Name {
			// Preserve user-provided fields, override the ones the operator owns.
			merged := containers[i]
			merged.Image = c.Image
			merged.Ports = c.Ports
			merged.Env = c.Env
			merged.VolumeMounts = c.VolumeMounts
			containers[i] = merged
			return containers
		}
	}
	return append(containers, c)
}

// upsertVolume replaces a volume with the same name, or appends it.
func upsertVolume(volumes []corev1.Volume, v corev1.Volume) []corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == v.Name {
			volumes[i] = v
			return volumes
		}
	}
	return append(volumes, v)
}

// SetupWithManager registers the NixStore controller with the manager.
func (r *NixStoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.NixStore{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Named("nixstore").
		Complete(r)
}
