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
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NixStatefulSetSpec is a Nix flake attribute run as an ordered, stateful
// workload that rolls out on a new revision. It compiles down to an apps/v1
// StatefulSet. serviceName, volumeClaimTemplates, updateStrategy,
// podManagementPolicy, and replicas all live natively in StatefulSetTemplate.
type NixStatefulSetSpec struct {
	// Nix is the flake source, revision tracking, and build/run configuration.
	Nix NixSpec `json:"nix"`

	// StatefulSetTemplate is the native apps/v1 StatefulSetSpec, verbatim.
	// Required (serviceName has no default). The operator owns the app container,
	// init-containers, nix volumes, revision annotation, labels, and (when unset)
	// the selector.
	StatefulSetTemplate appsv1.StatefulSetSpec `json:"statefulSetTemplate"`
}

// NixStatefulSetStatus is the observed state of a NixStatefulSet.
type NixStatefulSetStatus struct {
	NixWorkloadStatus `json:",inline"`

	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`
	// +optional
	CurrentRevision string `json:"currentRevision,omitempty"`
	// +optional
	UpdateRevision string `json:"updateRevision,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nixsts
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.rolledOutRevision`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NixStatefulSet runs a Nix flake attribute as an ordered Kubernetes StatefulSet.
type NixStatefulSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NixStatefulSetSpec   `json:"spec,omitempty"`
	Status NixStatefulSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NixStatefulSetList contains a list of NixStatefulSet.
type NixStatefulSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NixStatefulSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NixStatefulSet{}, &NixStatefulSetList{})
}
