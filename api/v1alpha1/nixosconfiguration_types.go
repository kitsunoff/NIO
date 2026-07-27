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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NixosConfigurationSpec defines the desired state of NixosConfiguration.
type NixosConfigurationSpec struct {
	// MachineRef is a reference to the target Machine resource.
	// Machine must be in the same namespace as NixosConfiguration (by design).
	MachineRef MachineReference `json:"machineRef"`

	// GitRepo is the URL of the git repository containing NixOS configuration.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	GitRepo string `json:"gitRepo,omitempty"`

	// Ref is the git reference (branch, tag, or commit) to checkout.
	// +kubebuilder:default="main"
	// +optional
	Ref string `json:"ref,omitempty"`

	// CredentialsRef references a Secret for private repository access.
	// Must be in the same namespace.
	// +optional
	CredentialsRef *SecretReference `json:"credentialsRef,omitempty"`

	// Flake is the flake reference (e.g., "#worker").
	// +optional
	Flake string `json:"flake,omitempty"`

	// OnRemoveFlake is the flake to apply when this resource is deleted.
	// +optional
	OnRemoveFlake string `json:"onRemoveFlake,omitempty"`

	// ConfigurationSubdir is the subdirectory containing Nix configuration.
	// +optional
	ConfigurationSubdir string `json:"configurationSubdir,omitempty"`

	// DayTwoSchedule is the cron schedule for the periodic day-2 convergence
	// (`nixos-rebuild switch`) that self-heals node drift. Defaults to every 30
	// minutes. On-revision-change applies fire promptly regardless (the day-2
	// NixCronJob uses triggerOnChange with concurrencyPolicy=Forbid).
	// +kubebuilder:default="*/30 * * * *"
	// +optional
	DayTwoSchedule string `json:"dayTwoSchedule,omitempty"`

	// FullInstall enables nixos-anywhere for full disk installation.
	// +optional
	FullInstall bool `json:"fullInstall,omitempty"`

	// AdditionalFiles are files to inject into the repository before apply.
	// +optional
	AdditionalFiles []AdditionalFile `json:"additionalFiles,omitempty"`

	// JobTemplate customizes the apply Job pods.
	// +optional
	JobTemplate *JobTemplate `json:"jobTemplate,omitempty"`

	// StoreRef adds a shared NixStore (same namespace) to the child workloads as
	// a SUBSTITUTER: paths the store already holds are fetched instead of built.
	// It is not a cache the children fill — a child pushes into the store only
	// when BuilderRef is set too, and then only the paths it builds directly.
	// StoreRef additionally supplies the SSH identity used to reach BuilderRef's
	// builder, so a builder with no store on either object cannot be dispatched
	// to. Optional; passed through to the install/day-2/decommission children.
	// +optional
	StoreRef *LocalObjectReference `json:"storeRef,omitempty"`

	// BuilderRef offloads the child workloads' builds to a shared NixBuilder
	// (same namespace) instead of building in the pod. This is what actually
	// speeds up day-two convergence — the closures are realized on the builder
	// and stay in the builder's /nix, so give that NixBuilder `spec.storage` or
	// its /nix is an emptyDir that dies with the pod. The builder must be able to
	// build the target's system: a NixBuilder without `spec.systems` is
	// advertised for both common Linux architectures, and delegation sets
	// `max-jobs = 0`, so there is no local fallback if it cannot.
	// +optional
	BuilderRef *LocalObjectReference `json:"builderRef,omitempty"`
}

// MachineReference references a Machine resource in the same namespace.
type MachineReference struct {
	// Name is the Machine resource name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AdditionalFile defines a file to inject into the repository.
type AdditionalFile struct {
	// Path is the file path relative to repository root.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path"`

	// ValueType specifies how to obtain the file content.
	// +kubebuilder:validation:Enum=Inline;SecretRef;NixosFacter
	ValueType AdditionalFileValueType `json:"valueType"`

	// Inline is the literal file content (for ValueType=Inline).
	// +optional
	Inline string `json:"inline,omitempty"`

	// SecretRef references a Secret key (for ValueType=SecretRef).
	// +optional
	SecretRef *SecretKeyReference `json:"secretRef,omitempty"`

	// NixosFacter generates content from Machine facts (for ValueType=NixosFacter).
	// +optional
	NixosFacter bool `json:"nixosFacter,omitempty"`
}

// AdditionalFileValueType specifies the source of additional file content.
// +kubebuilder:validation:Enum=Inline;SecretRef;NixosFacter
type AdditionalFileValueType string

const (
	// AdditionalFileValueTypeInline uses literal content from spec.
	AdditionalFileValueTypeInline AdditionalFileValueType = "Inline"

	// AdditionalFileValueTypeSecretRef gets content from a Secret.
	AdditionalFileValueTypeSecretRef AdditionalFileValueType = "SecretRef"

	// AdditionalFileValueTypeNixosFacter generates content from Machine facts.
	AdditionalFileValueTypeNixosFacter AdditionalFileValueType = "NixosFacter"
)

// SecretKeyReference references a specific key in a Secret.
type SecretKeyReference struct {
	// Name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the key in the Secret.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// JobTemplate defines customization for apply Job pods.
type JobTemplate struct {
	// Image is the container image for apply jobs.
	// If not specified, uses the operator's default image.
	// +optional
	Image string `json:"image,omitempty"`

	// NodeSelector is a selector for job pod assignment.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations are tolerations for job pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Resources are resource requirements for the job container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ServiceAccountName is the ServiceAccount for job pods.
	// If not specified, uses the default job ServiceAccount.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// NixosConfigurationStatus defines the observed state of NixosConfiguration.
type NixosConfigurationStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the coarse orchestrator state machine state
	// (Pending, Blocked, Installing, Converging, Ready, Degraded, Removing).
	// +optional
	Phase string `json:"phase,omitempty"`

	// FullDiskInstallCompleted indicates if nixos-anywhere was run.
	// +optional
	FullDiskInstallCompleted bool `json:"fullDiskInstallCompleted,omitempty"`

	// ResolvedRevision is the immutable commit SHA currently rolled out, sourced
	// from the day-2 child's status.rolledOutRevision.
	// +optional
	ResolvedRevision string `json:"resolvedRevision,omitempty"`

	// LastAppliedTime is the timestamp of last successful application.
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`

	// TargetMachine is the Machine resource name this config applies to.
	// +optional
	TargetMachine string `json:"targetMachine,omitempty"`

	// InstallJobRef is the name of the child install NixJob (nixos-anywhere).
	// +optional
	InstallJobRef string `json:"installJobRef,omitempty"`

	// DayTwoCronJobRef is the name of the child day-2 NixCronJob.
	// +optional
	DayTwoCronJobRef string `json:"dayTwoCronJobRef,omitempty"`

	// DecommissionJobRef is the name of the orphan decommission NixJob.
	// +optional
	DecommissionJobRef string `json:"decommissionJobRef,omitempty"`

	// InstallRetries bounds Installing retry.
	// +optional
	InstallRetries int32 `json:"installRetries,omitempty"`

	// OnRemoveRetries bounds decommission retry.
	// +optional
	OnRemoveRetries int32 `json:"onRemoveRetries,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// NixosConfiguration orchestrator phase values (status.phase). Named with the
// NixosConfig prefix to avoid clashing with the workload Phase* consts.
const (
	// NixosConfigPhasePending is the initial phase (finalizer added, resolving).
	NixosConfigPhasePending = "Pending"

	// NixosConfigPhaseBlocked means the target Machine is missing/undiscoverable
	// or already owned by another config; no children are driven forward.
	NixosConfigPhaseBlocked = "Blocked"

	// NixosConfigPhaseInstalling means the full-disk install NixJob is running.
	NixosConfigPhaseInstalling = "Installing"

	// NixosConfigPhaseConverging means the day-2 NixCronJob is created/updated
	// but not yet confirmed healthy.
	NixosConfigPhaseConverging = "Converging"

	// NixosConfigPhaseReady means the day-2 cron is healthy and applied.
	NixosConfigPhaseReady = "Ready"

	// NixosConfigPhaseDegraded means an install or day-2 run failed.
	NixosConfigPhaseDegraded = "Degraded"

	// NixosConfigPhaseRemoving means deletion is in progress (decommission).
	NixosConfigPhaseRemoving = "Removing"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.machineRef.name"
// +kubebuilder:printcolumn:name="Flake",type="string",JSONPath=".spec.flake"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NixosConfiguration is the Schema for the nixosconfigurations API.
type NixosConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of NixosConfiguration.
	// +required
	Spec NixosConfigurationSpec `json:"spec"`

	// Status defines the observed state of NixosConfiguration.
	// +optional
	Status NixosConfigurationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NixosConfigurationList contains a list of NixosConfiguration.
type NixosConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NixosConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NixosConfiguration{}, &NixosConfigurationList{})
}
