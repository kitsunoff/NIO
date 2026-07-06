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

	// FullInstall enables nixos-anywhere for full disk installation.
	// +optional
	FullInstall bool `json:"fullInstall,omitempty"`

	// AdditionalFiles are files to inject into the repository before apply.
	// +optional
	AdditionalFiles []AdditionalFile `json:"additionalFiles,omitempty"`

	// JobTemplate customizes the apply Job pods.
	// +optional
	JobTemplate *JobTemplate `json:"jobTemplate,omitempty"`
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

	// FullDiskInstallCompleted indicates if nixos-anywhere was run.
	// +optional
	FullDiskInstallCompleted bool `json:"fullDiskInstallCompleted,omitempty"`

	// AppliedCommit is the git commit hash that was applied.
	// +optional
	AppliedCommit string `json:"appliedCommit,omitempty"`

	// LastAppliedTime is the timestamp of last successful application.
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`

	// TargetMachine is the Machine resource name this config applies to.
	// +optional
	TargetMachine string `json:"targetMachine,omitempty"`

	// ConfigurationHash is the hash of applied configuration.
	// +optional
	ConfigurationHash string `json:"configurationHash,omitempty"`

	// AdditionalFilesHash is the hash of injected files.
	// +optional
	AdditionalFilesHash string `json:"additionalFilesHash,omitempty"`

	// OperationState tracks long-running operation progress.
	// +optional
	OperationState *OperationState `json:"operationState,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// OperationState tracks long-running operation progress.
type OperationState struct {
	// Type of operation in progress.
	// +kubebuilder:validation:Enum=NixosRebuild;FullInstall
	Type OperationType `json:"type"`

	// StartedAt is when the operation began.
	StartedAt metav1.Time `json:"startedAt"`

	// Phase describes current operation phase.
	// +optional
	Phase string `json:"phase,omitempty"`

	// JobName is the name of the Kubernetes Job running this operation.
	JobName string `json:"jobName"`

	// LastLogLine contains last line of job output for quick status.
	// +optional
	LastLogLine string `json:"lastLogLine,omitempty"`
}

// OperationType is the type of NixOS apply operation.
// +kubebuilder:validation:Enum=NixosRebuild;FullInstall
type OperationType string

const (
	// OperationTypeNixosRebuild uses nixos-rebuild switch for updates.
	OperationTypeNixosRebuild OperationType = "NixosRebuild"

	// OperationTypeFullInstall uses nixos-anywhere for full disk installation.
	OperationTypeFullInstall OperationType = "FullInstall"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.machineRef.name"
// +kubebuilder:printcolumn:name="Flake",type="string",JSONPath=".spec.flake"
// +kubebuilder:printcolumn:name="Commit",type="string",JSONPath=".status.appliedCommit",priority=1
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
