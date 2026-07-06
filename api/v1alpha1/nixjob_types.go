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
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NixJobSpec is a Nix flake attribute run to completion, re-run on a new
// revision. It compiles down to a batch/v1 Job.
type NixJobSpec struct {
	// Nix is the flake source, revision tracking, and build/run configuration.
	Nix NixSpec `json:"nix"`

	// JobTemplate is the native batch/v1 JobSpec, verbatim. The operator owns
	// the app container, the init-containers, the nix volumes, and the labels.
	// +optional
	JobTemplate *batchv1.JobSpec `json:"jobTemplate,omitempty"`
}

// NixJobStatus is the observed state of a NixJob.
type NixJobStatus struct {
	NixWorkloadStatus `json:",inline"`

	// ActiveJob is the name of the run-Job for the current revision.
	// +optional
	ActiveJob string `json:"activeJob,omitempty"`
	// +optional
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`
	// +optional
	Succeeded int32 `json:"succeeded,omitempty"`
	// +optional
	Failed int32 `json:"failed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nixj
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.rolledOutRevision`
// +kubebuilder:printcolumn:name="Succeeded",type=integer,JSONPath=`.status.succeeded`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NixJob runs a Nix flake attribute to completion as a Kubernetes Job.
type NixJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NixJobSpec   `json:"spec,omitempty"`
	Status NixJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NixJobList contains a list of NixJob.
type NixJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NixJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NixJob{}, &NixJobList{})
}
