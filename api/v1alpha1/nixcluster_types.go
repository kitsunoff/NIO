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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NixClusterSpec defines an abstract cluster: it groups Machines into nodeGroups,
// maps opaque per-group values onto each member, and drives ONE idempotent
// converge NixCronJob against the downstream flake-parts repo. NIO never
// interprets the cluster's meaning (k3s/proxmox/etc) — semantics live in the
// flake repo + its nixcluster modules.
type NixClusterSpec struct {
	// Source is the downstream flake-parts repo (the cluster). The converge
	// NixCronJob checks it out and runs its per-cluster app.
	Source NixSource `json:"source"`

	// SSHKeyRef is a cluster-wide SSH private key (same namespace) mounted into
	// the converge pod so it can reach every member host. The Secret must carry
	// an "ssh-privatekey" key.
	// +optional
	SSHKeyRef *SecretReference `json:"sshKeyRef,omitempty"`

	// AgeKeyRef is a sops age key (same namespace) mounted into the converge pod
	// (as an INPUT for secrets decryption). The Secret must carry a "keys.txt"
	// key. NIO-side secret generation/rotation is out of scope for the MVP.
	// +optional
	AgeKeyRef *SecretReference `json:"ageKeyRef,omitempty"`

	// StoreRef optionally points the converge pod at a NixStore (same namespace),
	// so its build artifacts persist across runs. Without it a fresh converge pod
	// rebuilds the whole member closure in an ephemeral in-pod /nix every run.
	// +optional
	StoreRef *LocalObjectReference `json:"storeRef,omitempty"`

	// BuilderRef optionally delegates the converge build to a NixBuilder (same
	// namespace), which realizes into StoreRef's NixStore. Pairs with StoreRef to
	// accelerate day-two converges (cached closure instead of an in-pod rebuild).
	// +optional
	BuilderRef *LocalObjectReference `json:"builderRef,omitempty"`

	// DayTwoSchedule is the converge cadence (cron schedule).
	// +kubebuilder:default="*/30 * * * *"
	// +optional
	DayTwoSchedule string `json:"dayTwoSchedule,omitempty"`

	// NodeGroups select Machines (by label) and map values onto them. A Machine
	// belongs to strictly one nodeGroup — the FIRST matching group in spec order
	// claims it; later groups exclude it.
	// +kubebuilder:validation:MinItems=1
	NodeGroups []NodeGroup `json:"nodeGroups"`
}

// NodeGroup selects a subset of Machines and maps opaque values onto each
// selected member. It carries no ordering/lifecycle — converge owns those.
type NodeGroup struct {
	// Name identifies the group within the cluster (used in status and node
	// files). Must be unique within the cluster.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Selector matches Machine metadata.labels in the cluster's namespace.
	Selector metav1.LabelSelector `json:"selector"`

	// Count optionally caps the group to a stable, sticky subset of the matching
	// Machines. Unset means all matching Machines are members.
	// +optional
	Count *int32 `json:"count,omitempty"`

	// Values is an OPAQUE, schemaless nested object mapped onto each member as a
	// nixcluster member attrset (via recursiveUpdate of fromJSON). NIO never
	// interprets it. It must NOT carry nixosConfiguration (a Nix value): members
	// inherit the cluster-level default in the flake repo.
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// NixClusterStatus is the observed state of a NixCluster.
type NixClusterStatus struct {
	// Phase is a coarse, human-facing lifecycle state.
	// +kubebuilder:validation:Enum=Ready;Converging;Degraded;Blocked
	// +optional
	Phase string `json:"phase,omitempty"`

	// NodeGroups reflects the stable, sticky selection per group.
	// +optional
	NodeGroups []NodeGroupStatus `json:"nodeGroups,omitempty"`

	// ConvergeJobRef is the name of the owned converge NixCronJob.
	// +optional
	ConvergeJobRef string `json:"convergeJobRef,omitempty"`

	// Conditions: Ready, Stalled, GitSynced, Underprovisioned.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ObservedGeneration is the spec generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// NodeGroupStatus reflects a group's selection and per-member status.
type NodeGroupStatus struct {
	// Name is the nodeGroup name.
	Name string `json:"name"`

	// Members are the selected Machines, STABLE and sorted by name.
	// +optional
	Members []MemberStatus `json:"members,omitempty"`

	// Desired is the requested member count. When Count is unset (all matching
	// Machines are members), it reflects the number of matching candidates.
	// +optional
	Desired int32 `json:"desired"`

	// Selected is the actual number of members.
	// +optional
	Selected int32 `json:"selected"`
}

// MemberStatus is a selected Machine's per-node convergence status.
type MemberStatus struct {
	// Name is the Machine name.
	Name string `json:"name"`

	// Status is the per-node convergence status (best-effort; reflected from the
	// last converge run).
	// +optional
	Status string `json:"status,omitempty"`
}

// NixCluster phase values.
const (
	NixClusterPhaseReady      = "Ready"
	NixClusterPhaseConverging = "Converging"
	NixClusterPhaseDegraded   = "Degraded"
	NixClusterPhaseBlocked    = "Blocked"
)

// Per-member status values.
const (
	MemberStatusPending  = "Pending"
	MemberStatusApplying = "Applying"
	MemberStatusApplied  = "Applied"
	MemberStatusFailed   = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Converge",type=string,JSONPath=`.status.convergeJobRef`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NixCluster is an abstract grouping of Machines driven by one idempotent converge.
type NixCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NixClusterSpec   `json:"spec,omitempty"`
	Status NixClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NixClusterList contains a list of NixCluster.
type NixClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NixCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NixCluster{}, &NixClusterList{})
}
