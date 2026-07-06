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

// NixDeploymentSpec is a Nix flake attribute run as a long-running service that
// rolls out on a new revision. It compiles down to an apps/v1 Deployment.
type NixDeploymentSpec struct {
	// Nix is the flake source, revision tracking, and build/run configuration.
	Nix NixSpec `json:"nix"`

	// DeploymentTemplate is the native apps/v1 DeploymentSpec, verbatim. The
	// operator owns the app container image/command, the three init-containers,
	// the nix volumes, the pod-template revision annotation and managed labels,
	// and (when unset) the selector and surge-only strategy default. It is
	// schemaless so a minimal workload can omit the otherwise-required selector
	// and pod containers, which the reconciler fills (design §3.3, O7/ADR-0005).
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	DeploymentTemplate *appsv1.DeploymentSpec `json:"deploymentTemplate,omitempty"`
}

// NixDeploymentStatus is the observed state of a NixDeployment.
type NixDeploymentStatus struct {
	NixWorkloadStatus `json:",inline"`

	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`
	// +optional
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nixdeploy
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.rolledOutRevision`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NixDeployment runs a Nix flake attribute as a rolling Kubernetes Deployment.
type NixDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NixDeploymentSpec   `json:"spec,omitempty"`
	Status NixDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NixDeploymentList contains a list of NixDeployment.
type NixDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NixDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NixDeployment{}, &NixDeploymentList{})
}
