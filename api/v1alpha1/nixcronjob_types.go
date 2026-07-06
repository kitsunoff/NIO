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

// NixCronJobSpec is a Nix flake attribute run on a schedule, optionally also on
// a new revision. It compiles down to a batch/v1 CronJob. Scheduling,
// concurrencyPolicy, suspend, history limits, and the embedded jobTemplate all
// live natively in CronJobTemplate.
type NixCronJobSpec struct {
	// Nix is the flake source, revision tracking, and build/run configuration.
	Nix NixSpec `json:"nix"`

	// CronJobTemplate is the native batch/v1 CronJobSpec, verbatim. Required
	// (schedule has no default). The operator pins its jobTemplate to the latest
	// resolved revision and owns the app container, init-containers, and volumes.
	CronJobTemplate batchv1.CronJobSpec `json:"cronJobTemplate"`
}

// NixCronJobStatus is the observed state of a NixCronJob.
type NixCronJobStatus struct {
	NixWorkloadStatus `json:",inline"`

	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
	// +optional
	LastSuccessfulTime *metav1.Time `json:"lastSuccessfulTime,omitempty"`
	// +optional
	ActiveJobs []string `json:"activeJobs,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nixcron
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.cronJobTemplate.schedule`
// +kubebuilder:printcolumn:name="LastSchedule",type=date,JSONPath=`.status.lastScheduleTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NixCronJob runs a Nix flake attribute on a schedule as a Kubernetes CronJob.
type NixCronJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NixCronJobSpec   `json:"spec,omitempty"`
	Status NixCronJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NixCronJobList contains a list of NixCronJob.
type NixCronJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NixCronJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NixCronJob{}, &NixCronJobList{})
}
