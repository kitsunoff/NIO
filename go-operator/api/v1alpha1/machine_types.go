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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// MachineSpec defines the desired state of Machine.
type MachineSpec struct {
	// Host is the target machine address (hostname or IP) for SSH connection.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9][a-zA-Z0-9\-\.\:]*[a-zA-Z0-9]$|^[a-zA-Z0-9]$`
	Host string `json:"host"`

	// SSHUser is the SSH username for connection.
	// +kubebuilder:default="root"
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_][a-zA-Z0-9_\-]*$`
	// +optional
	SSHUser string `json:"sshUser,omitempty"`

	// SSHKeySecretRef references a Secret containing SSH private key.
	// The Secret must be in the same namespace as the Machine resource.
	// +optional
	SSHKeySecretRef *SecretReference `json:"sshKeySecretRef,omitempty"`

	// SSHPasswordSecretRef references a Secret containing SSH password.
	// The Secret must be in the same namespace as the Machine resource.
	// +optional
	SSHPasswordSecretRef *SSHPasswordSecretRef `json:"sshPasswordSecretRef,omitempty"`
}

// SecretReference references a Secret in the same namespace.
// Cross-namespace references are not supported by design.
type SecretReference struct {
	// Name is the Secret name (must be in the same namespace as the referencing resource).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// SSHPasswordSecretRef references a specific key in a Secret for SSH password.
// Must be in the same namespace as the Machine resource.
type SSHPasswordSecretRef struct {
	// Name is the Secret name (must be in the same namespace as the Machine).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the key in the Secret containing the password.
	// +kubebuilder:default="password"
	// +optional
	Key string `json:"key,omitempty"`
}

// MachineStatus defines the observed state of Machine.
type MachineStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Discoverable indicates if machine is reachable via SSH.
	// +optional
	Discoverable bool `json:"discoverable,omitempty"`

	// HasConfiguration indicates if a NixOS configuration is applied.
	// +optional
	HasConfiguration bool `json:"hasConfiguration,omitempty"`

	// AppliedConfiguration is the name of applied NixosConfiguration.
	// +optional
	AppliedConfiguration string `json:"appliedConfiguration,omitempty"`

	// AppliedCommit is the git commit hash of applied configuration.
	// +optional
	AppliedCommit string `json:"appliedCommit,omitempty"`

	// LastAppliedTime is the timestamp of last successful application.
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`

	// LastHardwareScanTime is the timestamp of last hardware scan.
	// +optional
	LastHardwareScanTime *metav1.Time `json:"lastHardwareScanTime,omitempty"`

	// HardwareFacts contains collected hardware information.
	// +optional
	HardwareFacts *HardwareFacts `json:"hardwareFacts,omitempty"`

	// NixFacterResult contains nix facter command output.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	NixFacterResult *runtime.RawExtension `json:"nixFacterResult,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// HardwareFacts contains hardware information collected from the machine.
type HardwareFacts struct {
	// OS contains operating system information.
	// +optional
	OS *OSInfo `json:"os,omitempty"`

	// Kernel contains kernel information.
	// +optional
	Kernel *KernelInfo `json:"kernel,omitempty"`

	// CPU contains processor information.
	// +optional
	CPU *CPUInfo `json:"cpu,omitempty"`

	// Memory contains memory information in MB.
	// +optional
	Memory *MemoryInfo `json:"memory,omitempty"`

	// Architecture is the system architecture (e.g., x86_64, aarch64).
	// +optional
	Architecture string `json:"architecture,omitempty"`

	// Hostname is the system hostname.
	// +optional
	Hostname string `json:"hostname,omitempty"`

	// Virtualization contains virtualization type information.
	// +optional
	Virtualization *VirtualizationInfo `json:"virtualization,omitempty"`

	// Disks contains disk information (name -> size).
	// +optional
	Disks map[string]string `json:"disks,omitempty"`

	// Interfaces contains network interface information (name -> IP).
	// +optional
	Interfaces map[string]string `json:"interfaces,omitempty"`
}

// OSInfo contains operating system information.
type OSInfo struct {
	// Name is the OS name (e.g., "NixOS").
	// +optional
	Name string `json:"name,omitempty"`

	// ID is the OS identifier (e.g., "nixos").
	// +optional
	ID string `json:"id,omitempty"`
}

// KernelInfo contains kernel information.
type KernelInfo struct {
	// Version is the kernel version.
	// +optional
	Version string `json:"version,omitempty"`
}

// CPUInfo contains CPU information.
type CPUInfo struct {
	// Model is the CPU model name.
	// +optional
	Model string `json:"model,omitempty"`

	// Cores is the number of CPU cores.
	// +optional
	Cores string `json:"cores,omitempty"`
}

// MemoryInfo contains memory information.
type MemoryInfo struct {
	// MB is the total memory in megabytes.
	// +optional
	MB string `json:"mb,omitempty"`
}

// VirtualizationInfo contains virtualization information.
type VirtualizationInfo struct {
	// Type is the virtualization type (physical, vm, docker, etc.).
	// +optional
	Type string `json:"type,omitempty"`

	// ContainerEngine is the container engine if running in a container.
	// +optional
	ContainerEngine string `json:"containerEngine,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Host",type="string",JSONPath=".spec.host"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Discoverable",type="string",JSONPath=".status.conditions[?(@.type==\"Discoverable\")].status"
// +kubebuilder:printcolumn:name="Config",type="string",JSONPath=".status.appliedConfiguration"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Machine is the Schema for the machines API.
type Machine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of Machine.
	// +required
	Spec MachineSpec `json:"spec"`

	// Status defines the observed state of Machine.
	// +optional
	Status MachineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MachineList contains a list of Machine.
type MachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Machine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Machine{}, &MachineList{})
}
