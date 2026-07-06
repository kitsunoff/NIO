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

// NixStoreSpec manages a StatefulSet running a centralized Nix store /
// binary-cache SERVER backed by a PVC. The server owns the store database;
// clients (runner pods, builders) reach it over the network as ordinary nix
// substituter/store clients. NixStore is namespace-scoped and only consumable by
// workloads in the same namespace.
type NixStoreSpec struct {
	// Replicas of the store-server StatefulSet.
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Storage is the volumeClaimTemplate backing the store (the real /nix).
	Storage corev1.PersistentVolumeClaimSpec `json:"storage"`

	// Image of the store server (a nix daemon + substituter server such as
	// harmonia / nix-serve). Defaults to an operator-provided image.
	// +optional
	Image string `json:"image,omitempty"`

	// SigningKeySecretRef holds the Nix signing key pair used to sign served
	// paths. If absent, the operator generates one and publishes the public key
	// in status.
	// +optional
	SigningKeySecretRef *SecretReference `json:"signingKeySecretRef,omitempty"`

	// UpstreamSubstituters the store falls through to on a miss (so builds layer
	// on top of cache.nixos.org). cache.nixos.org is trusted by default.
	// +optional
	UpstreamSubstituters []string `json:"upstreamSubstituters,omitempty"`

	// Template overrides for the server pods (resources, scheduling, …).
	// +optional
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`
}

// NixStoreStatus is the observed state of a NixStore.
type NixStoreStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +kubebuilder:validation:Enum=Pending;Ready;Degraded
	// +optional
	Phase string `json:"phase,omitempty"`

	// SubstituterURL is the READ endpoint clients pass to `--substituters`.
	// +optional
	SubstituterURL string `json:"substituterURL,omitempty"`

	// StoreURI is the realize/push endpoint (e.g. ssh-ng://svc or the daemon).
	// +optional
	StoreURI string `json:"storeURI,omitempty"`

	// PublicKey is the trusted-public-key entry clients must trust.
	// +optional
	PublicKey string `json:"publicKey,omitempty"`

	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nstore
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Substituter",type=string,JSONPath=`.status.substituterURL`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NixStore is a centralized Nix store / binary-cache server for a namespace.
type NixStore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NixStoreSpec   `json:"spec,omitempty"`
	Status NixStoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NixStoreList contains a list of NixStore.
type NixStoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NixStore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NixStore{}, &NixStoreList{})
}
