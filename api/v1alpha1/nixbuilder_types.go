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

// NixBuilderSpec manages a StatefulSet with a SINGLE builder worker that runs
// `nix build`. MVP-deliberate: one worker, no pool, no routing/balancing.
// Builds offload here via nix remote-build; the builder realizes INTO its
// StoreRef NixStore, so outputs are immediately available to that store's
// substituter clients.
type NixBuilderSpec struct {
	// StoreRef is the NixStore this builder realizes into. For a
	// nix.builderTemplate it defaults to the workload's nix.storeRef.
	// +optional
	StoreRef *LocalObjectReference `json:"storeRef,omitempty"`

	// Image of the builder pod (nix + toolchain). Defaults to the nix image.
	// +optional
	Image string `json:"image,omitempty"`

	// Systems this builder can build (e.g. ["x86_64-linux","aarch64-linux"]).
	// +optional
	Systems []string `json:"systems,omitempty"`

	// MaxJobs is nix `max-jobs` on the single worker (how many builds it runs in
	// parallel). This is the only concurrency knob — there is no horizontal pool.
	// +optional
	MaxJobs *int32 `json:"maxJobs,omitempty"`

	// Storage is the builder's own persistent /nix (the StatefulSet's
	// volumeClaimTemplate), so its build cache survives restarts.
	// +optional
	Storage *corev1.PersistentVolumeClaimSpec `json:"storage,omitempty"`

	// Template overrides for the builder pod (big resources, taints, …).
	// +optional
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`
}

// NixBuilderStatus is the observed state of a NixBuilder.
type NixBuilderStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +kubebuilder:validation:Enum=Pending;Ready;Degraded
	// +optional
	Phase string `json:"phase,omitempty"`

	// BuilderEndpoint is the remote-build endpoint (e.g. ssh-ng://svc) that
	// builds dispatch to.
	// +optional
	BuilderEndpoint string `json:"builderEndpoint,omitempty"`

	// Ready is true when the single worker is available.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nbuilder
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.builderEndpoint`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NixBuilder is a single-worker remote Nix build backend for a namespace.
type NixBuilder struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NixBuilderSpec   `json:"spec,omitempty"`
	Status NixBuilderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NixBuilderList contains a list of NixBuilder.
type NixBuilderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NixBuilder `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NixBuilder{}, &NixBuilderList{})
}
