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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
	"github.com/kitsunoff/nixos-operator/internal/metrics"
	"github.com/kitsunoff/nixos-operator/internal/ssh"
)

const (
	// DefaultSSHPort is the default SSH port.
	DefaultSSHPort = 22

	// DefaultSSHTimeout is the default SSH connection timeout.
	DefaultSSHTimeout = 30 * time.Second

	// DiscoveryInterval is the interval between discovery checks.
	DiscoveryInterval = 60 * time.Second

	// IndexMachineBySSHKeySecret is the field index for SSH key secret references.
	IndexMachineBySSHKeySecret = "spec.sshKeySecretRef.name"

	// IndexMachineBySSHPasswordSecret is the field index for SSH password secret references.
	IndexMachineBySSHPasswordSecret = "spec.sshPasswordSecretRef.name"
)

// MachineReconciler reconciles a Machine object.
type MachineReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	SSHClient ssh.Client
}

// +kubebuilder:rbac:groups=nio.homystack.com,resources=machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nio.homystack.com,resources=machines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nio.homystack.com,resources=machines/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main reconciliation loop for Machine resources.
func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Machine instance
	var machine niov1alpha1.Machine
	if err := r.Get(ctx, req.NamespacedName, &machine); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Set observedGeneration immediately
	machine.Status.ObservedGeneration = machine.Generation

	// Set Reconciling condition to True
	meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
		Type:               niov1alpha1.ConditionReconciling,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: machine.Generation,
		Reason:             niov1alpha1.ReasonProgressing,
		Message:            "Reconciliation in progress",
	})

	// Update status early
	if err := r.Status().Update(ctx, &machine); err != nil {
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !machine.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &machine)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&machine, niov1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(&machine, niov1alpha1.FinalizerName)
		if err := r.Update(ctx, &machine); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Perform reconciliation
	result, reconcileErr := r.reconcile(ctx, &machine)

	// Set final conditions based on result
	if reconcileErr != nil {
		log.Error(reconcileErr, "reconciliation failed")
		meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionStalled,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: machine.Generation,
			Reason:             niov1alpha1.ReasonFailed,
			Message:            reconcileErr.Error(),
		})
		meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: machine.Generation,
			Reason:             niov1alpha1.ReasonFailed,
			Message:            reconcileErr.Error(),
		})
	} else {
		meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionReconciling,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: machine.Generation,
			Reason:             niov1alpha1.ReasonSucceeded,
			Message:            "Reconciliation completed",
		})
		meta.RemoveStatusCondition(&machine.Status.Conditions, niov1alpha1.ConditionStalled)

		// Set Ready based on Discoverable status
		if machine.Status.Discoverable {
			meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
				Type:               niov1alpha1.ConditionReady,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: machine.Generation,
				Reason:             niov1alpha1.ReasonSSHConnected,
				Message:            "Machine is ready and reachable via SSH",
			})
		} else {
			meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
				Type:               niov1alpha1.ConditionReady,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: machine.Generation,
				Reason:             niov1alpha1.ReasonSSHFailed,
				Message:            "Machine is not reachable via SSH",
			})
		}
	}

	// Final status update
	if err := r.Status().Update(ctx, &machine); err != nil {
		return ctrl.Result{}, err
	}

	return result, reconcileErr
}

// reconcile performs the main reconciliation logic.
//
//nolint:unparam // error return kept for controller-runtime pattern consistency
func (r *MachineReconciler) reconcile(ctx context.Context, machine *niov1alpha1.Machine) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check SSH connectivity
	discoverable, err := r.checkDiscoverable(ctx, machine)
	if err != nil {
		log.Error(err, "failed to check discoverability")
		// Don't return error - this is expected when machine is unreachable
	}

	machine.Status.Discoverable = discoverable

	if discoverable {
		meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionDiscoverable,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: machine.Generation,
			Reason:             niov1alpha1.ReasonSSHConnected,
			Message:            "SSH connection successful",
		})
		r.Recorder.Event(machine, corev1.EventTypeNormal, "Discoverable", "Machine is reachable via SSH")
	} else {
		// Only set SSHFailed if checkDiscoverable didn't already set a more specific reason
		// (e.g., CredentialsMissing was set when secret was not found)
		discoverableCondition := meta.FindStatusCondition(machine.Status.Conditions, niov1alpha1.ConditionDiscoverable)
		if discoverableCondition == nil || discoverableCondition.Reason != niov1alpha1.ReasonCredentialsMissing {
			message := "SSH connection failed"
			if err != nil {
				message = err.Error()
			}
			meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
				Type:               niov1alpha1.ConditionDiscoverable,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: machine.Generation,
				Reason:             niov1alpha1.ReasonSSHFailed,
				Message:            message,
			})
		}
	}

	// Requeue for periodic check
	return ctrl.Result{RequeueAfter: DiscoveryInterval}, nil
}

// checkDiscoverable tests SSH connectivity to the machine.
func (r *MachineReconciler) checkDiscoverable(ctx context.Context, machine *niov1alpha1.Machine) (bool, error) {
	log := logf.FromContext(ctx)

	// Build SSH config
	sshConfig, err := r.buildSSHConfig(ctx, machine)
	if err != nil {
		log.Error(err, "failed to build SSH config")
		meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
			Type:               niov1alpha1.ConditionDiscoverable,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: machine.Generation,
			Reason:             niov1alpha1.ReasonCredentialsMissing,
			Message:            err.Error(),
		})
		r.Recorder.Event(machine, corev1.EventTypeWarning, "CredentialsMissing", err.Error())
		metrics.RecordError("ssh")
		return false, err
	}

	// Create context with timeout
	checkCtx, cancel := context.WithTimeout(ctx, DefaultSSHTimeout)
	defer cancel()

	// Check connection and record metrics
	startTime := time.Now()
	if err := r.SSHClient.CheckConnection(checkCtx, machine.Spec.Host, DefaultSSHPort, sshConfig); err != nil {
		log.Info("SSH connection failed", "host", machine.Spec.Host, "error", err)
		metrics.RecordSSHConnection(false, time.Since(startTime).Seconds())
		return false, err
	}

	metrics.RecordSSHConnection(true, time.Since(startTime).Seconds())
	log.Info("SSH connection successful", "host", machine.Spec.Host)
	return true, nil
}

// buildSSHConfig creates SSH configuration from Machine spec and secrets.
func (r *MachineReconciler) buildSSHConfig(ctx context.Context, machine *niov1alpha1.Machine) (*ssh.Config, error) {
	config := &ssh.Config{
		User:    machine.Spec.SSHUser,
		Timeout: DefaultSSHTimeout,
	}

	// Default user to root if not specified
	if config.User == "" {
		config.User = "root"
	}

	// Try to get SSH key from secret
	if machine.Spec.SSHKeySecretRef != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      machine.Spec.SSHKeySecretRef.Name,
			Namespace: machine.Namespace,
		}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("SSH key secret %q not found", machine.Spec.SSHKeySecretRef.Name)
			}
			return nil, fmt.Errorf("get SSH key secret: %w", err)
		}

		privateKey, ok := secret.Data["ssh-privatekey"]
		if !ok {
			return nil, fmt.Errorf("secret %q does not contain 'ssh-privatekey'", machine.Spec.SSHKeySecretRef.Name)
		}
		config.PrivateKey = privateKey
	}

	// Try to get SSH password from secret
	if machine.Spec.SSHPasswordSecretRef != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      machine.Spec.SSHPasswordSecretRef.Name,
			Namespace: machine.Namespace,
		}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("SSH password secret %q not found", machine.Spec.SSHPasswordSecretRef.Name)
			}
			return nil, fmt.Errorf("get SSH password secret: %w", err)
		}

		key := machine.Spec.SSHPasswordSecretRef.Key
		if key == "" {
			key = "password"
		}
		password, ok := secret.Data[key]
		if !ok {
			return nil, fmt.Errorf("secret %q does not contain key %q", machine.Spec.SSHPasswordSecretRef.Name, key)
		}
		config.Password = string(password)
	}

	// Ensure at least one authentication method is configured
	if len(config.PrivateKey) == 0 && config.Password == "" {
		return nil, fmt.Errorf("no SSH authentication method configured (neither key nor password)")
	}

	return config, nil
}

// reconcileDelete handles deletion of the Machine resource.
func (r *MachineReconciler) reconcileDelete(ctx context.Context, machine *niov1alpha1.Machine) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("handling machine deletion")

	// Check if any NixosConfiguration references this machine
	// TODO: Implement blocking deletion if configurations exist

	// Remove finalizer
	if controllerutil.ContainsFinalizer(machine, niov1alpha1.FinalizerName) {
		controllerutil.RemoveFinalizer(machine, niov1alpha1.FinalizerName)
		if err := r.Update(ctx, machine); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// findMachinesForSecret returns reconcile requests for all Machines that reference the given Secret.
func (r *MachineReconciler) findMachinesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)
	secret := obj.(*corev1.Secret)

	// Find machines referencing this secret as SSH key
	var machineList niov1alpha1.MachineList
	if err := r.List(ctx, &machineList,
		client.InNamespace(secret.Namespace),
		client.MatchingFields{IndexMachineBySSHKeySecret: secret.Name},
	); err != nil {
		log.Error(err, "failed to list machines by SSH key secret")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(machineList.Items))

	for _, machine := range machineList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      machine.Name,
				Namespace: machine.Namespace,
			},
		})
	}

	// Find machines referencing this secret as SSH password
	if err := r.List(ctx, &machineList,
		client.InNamespace(secret.Namespace),
		client.MatchingFields{IndexMachineBySSHPasswordSecret: secret.Name},
	); err != nil {
		log.Error(err, "failed to list machines by SSH password secret")
		return requests
	}

	for _, machine := range machineList.Items {
		// Avoid duplicates
		found := false
		for _, req := range requests {
			if req.Name == machine.Name && req.Namespace == machine.Namespace {
				found = true
				break
			}
		}
		if !found {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      machine.Name,
					Namespace: machine.Namespace,
				},
			})
		}
	}

	if len(requests) > 0 {
		log.Info("found machines for secret", "secret", secret.Name, "count", len(requests))
	}

	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Set up field indexes for secret watches
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &niov1alpha1.Machine{},
		IndexMachineBySSHKeySecret,
		func(obj client.Object) []string {
			machine := obj.(*niov1alpha1.Machine)
			if machine.Spec.SSHKeySecretRef == nil {
				return nil
			}
			return []string{machine.Spec.SSHKeySecretRef.Name}
		},
	); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &niov1alpha1.Machine{},
		IndexMachineBySSHPasswordSecret,
		func(obj client.Object) []string {
			machine := obj.(*niov1alpha1.Machine)
			if machine.Spec.SSHPasswordSecretRef == nil {
				return nil
			}
			return []string{machine.Spec.SSHPasswordSecretRef.Name}
		},
	); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&niov1alpha1.Machine{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findMachinesForSecret),
		).
		Named("machine").
		Complete(r)
}
