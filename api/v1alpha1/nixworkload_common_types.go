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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LocalObjectReference references another object in the SAME namespace by name.
// Cross-namespace references are out of scope for v1alpha1 (see design §6).
type LocalObjectReference struct {
	// Name of the referenced object (must be in the same namespace).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// NixSource describes the flake's git origin and how the revision is tracked.
type NixSource struct {
	// GitRepo is the flake repository URL (https or ssh form).
	// Ignored when FluxSourceRef is set.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	GitRepo string `json:"gitRepo,omitempty"`

	// Ref is the mutable git ref (branch or tag) the operator tracks.
	// Polling re-resolves this to a commit SHA on PollInterval.
	// +kubebuilder:default="main"
	// +optional
	Ref string `json:"ref,omitempty"`

	// Rev pins an exact commit SHA. When set, polling is disabled and this
	// revision is used verbatim (GitOps-pinned / immutable deployments).
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{7,40}$`
	// +optional
	Rev string `json:"rev,omitempty"`

	// Dir is an optional subdirectory holding the flake (flake-in-subdir).
	// +optional
	Dir string `json:"dir,omitempty"`

	// CredentialsRef references a Secret (same namespace) for private repo
	// access. Recognised keys: "username"+"password", or "ssh-privatekey".
	// +optional
	CredentialsRef *SecretReference `json:"credentialsRef,omitempty"`

	// FluxSourceRef, when set, makes the operator consume a Flux source object
	// (source.toolkit.fluxcd.io) in the SAME namespace instead of polling git
	// itself. Any artifact-producing Flux kind is accepted — GitRepository,
	// OCIRepository, or Bucket — because they share one status.artifact contract.
	// The operator reads status.artifact.revision (the rollout key) and
	// status.artifact.url (the tarball the pod fetches). GitRepo / Ref /
	// PollInterval / CredentialsRef are then ignored.
	// +optional
	FluxSourceRef *FluxSourceRef `json:"fluxSourceRef,omitempty"`

	// PollInterval controls how often Ref is re-resolved via `git ls-remote`.
	// Ignored when Rev or FluxSourceRef is set.
	// +kubebuilder:default="1m"
	// +optional
	PollInterval *metav1.Duration `json:"pollInterval,omitempty"`
}

// FluxSourceRef points at a Flux source.toolkit.fluxcd.io object whose
// status.artifact (url + revision) the operator consumes. All three accepted
// kinds expose the same artifact contract, so the operator treats them
// uniformly: it never inspects the source-type-specific spec.
type FluxSourceRef struct {
	// Kind of the referenced Flux source.
	// +kubebuilder:validation:Enum=GitRepository;OCIRepository;Bucket
	Kind string `json:"kind"`

	// Name of the Flux source object (same namespace as this workload).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// APIVersion of the Flux source group/version. Defaults to the
	// source.toolkit.fluxcd.io version the operator is built against.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
}

// NixSpec groups everything Nix- and git-specific. The Kubernetes workload
// shape lives in the sibling <kind>Template field, never here. WHERE things
// build and run is just two references — a NixStore and (optionally) a
// NixBuilder — never inline store/cache config.
type NixSpec struct {
	// Source is the flake's git origin and revision tracking.
	Source NixSource `json:"source"`

	// Run is the installable to execute, exactly as typed after `nix run`.
	// The source is checked out into the working directory at the resolved
	// revision, so "." refers to it. Defaults to "." (the source flake's
	// default app).
	// +kubebuilder:default="."
	// +optional
	Run string `json:"run,omitempty"`

	// Args follow the `--` separator: runtime data, NOT built. "." here also
	// refers to the checked-out source.
	// +optional
	Args []string `json:"args,omitempty"`

	// Prebuild lists flake installables to materialize BEFORE the app runs, in
	// addition to Run. Each entry is a flake installable — local (".#dep") or
	// external ("github:owner/repo#x"). The instantiate init-container runs
	// `nix build <Run> <Prebuild...>`; if any fails to build, the init fails and
	// the pod does not start (per-pod fail-fast).
	// +optional
	Prebuild []string `json:"prebuild,omitempty"`

	// Image is the nix-bearing container image for the runner pods (it provides
	// the nix CLI seeded by the bootstrap init, and runs the build in the
	// instantiate init). Defaults to the official upstream nix image.
	// +kubebuilder:default="nixos/nix:latest"
	// +optional
	Image string `json:"image,omitempty"`

	// ContainerName is the container in <kind>Template the operator OWNS: it
	// sets that container's .image and .command and wires the local /nix +
	// workdir. If absent in the template, it is synthesized.
	// +kubebuilder:default=app
	// +optional
	ContainerName string `json:"containerName,omitempty"`

	// NixFlags are extra nix CLI flags for build and run. nix-command/flakes
	// are always enabled.
	// +optional
	NixFlags []string `json:"nixFlags,omitempty"`

	// StoreRef points the workload's nix at a NixStore (§3.5) in the SAME
	// namespace: runner pods use it as a `--substituters` source. When ABSENT
	// (and no BuilderRef), build+run is ephemeral and pod-local — a dev fallback.
	// +optional
	StoreRef *LocalObjectReference `json:"storeRef,omitempty"`

	// BuilderRef offloads the build to a SHARED, single-worker NixBuilder (§3.6)
	// in the SAME namespace. Mutually exclusive with BuilderTemplate.
	// +optional
	BuilderRef *LocalObjectReference `json:"builderRef,omitempty"`

	// BuilderTemplate constructs a DEDICATED single-worker NixBuilder owned by
	// this workload (its StoreRef defaults to this workload's StoreRef).
	// Mutually exclusive with BuilderRef. When neither is set, each pod builds
	// locally in its own init-container.
	// +optional
	BuilderTemplate *NixBuilderSpec `json:"builderTemplate,omitempty"`

	// LocalStore configures the pod-local /nix the realized closure is
	// substituted into. Defaults to a node-local emptyDir.
	// +optional
	LocalStore *NixLocalStore `json:"localStore,omitempty"`

	// TriggerOnChange controls the reaction to a NEW resolved revision.
	// Per-kind meaning (and per-kind default):
	//   NixDeployment/NixStatefulSet: roll out the new revision   (default true)
	//   NixJob:                       create a fresh run-Job       (default true)
	//   NixCronJob:                   also fire an immediate Job    (default false)
	// +optional
	TriggerOnChange *bool `json:"triggerOnChange,omitempty"`

	// Suspend pauses NIO reconciliation (no new builds / rollouts / runs).
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// NixLocalStore configures the pod-local /nix that the realized closure is
// substituted into. The content is reproducible and lives in the NixStore, so
// this volume need NOT survive a reschedule — emptyDir vs a per-pod generic
// ephemeral volume is purely a size/perf choice.
type NixLocalStore struct {
	// Medium:
	//   "Disk"   — emptyDir on node disk (DEFAULT): fast, node-local.
	//   "Memory" — emptyDir tmpfs: fastest, RAM-bound; tiny closures only.
	//   "PodPVC" — a per-pod PVC (provisioned as a generic ephemeral volume,
	//              deleted with the pod) on StorageClassName.
	// +kubebuilder:validation:Enum=Disk;Memory;PodPVC
	// +kubebuilder:default=Disk
	// +optional
	Medium string `json:"medium,omitempty"`

	// SizeLimit caps the emptyDir (Disk/Memory) or requests the size for a
	// PodPVC volume.
	// +optional
	SizeLimit *resource.Quantity `json:"sizeLimit,omitempty"`

	// StorageClassName selects the storage class for Medium=PodPVC
	// (defaults to the cluster default class).
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`
}

// NixWorkloadStatus is embedded by every NIO workload status.
type NixWorkloadStatus struct {
	// ObservedGeneration is the spec generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse, human-facing lifecycle state.
	// +kubebuilder:validation:Enum=Pending;Resolving;Building;Progressing;Ready;Degraded;Failed;Suspended
	// +optional
	Phase string `json:"phase,omitempty"`

	// ResolvedRevision is the commit SHA that Ref currently points to.
	// +optional
	ResolvedRevision string `json:"resolvedRevision,omitempty"`

	// LastPolledTime is when Ref was last resolved.
	// +optional
	LastPolledTime *metav1.Time `json:"lastPolledTime,omitempty"`

	// RolledOutRevision is the commit SHA currently stamped into the owned
	// workload's pod template — i.e. the revision its pods build and run.
	// +optional
	RolledOutRevision string `json:"rolledOutRevision,omitempty"`

	// WorkloadRef is the name of the owned native workload object.
	// +optional
	WorkloadRef string `json:"workloadRef,omitempty"`

	// Conditions: Ready, Reconciling, Stalled (kstatus) + GitSynced, Progressing.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// Nix workload phase values.
const (
	PhasePending     = "Pending"
	PhaseResolving   = "Resolving"
	PhaseBuilding    = "Building"
	PhaseProgressing = "Progressing"
	PhaseReady       = "Ready"
	PhaseDegraded    = "Degraded"
	PhaseFailed      = "Failed"
	PhaseSuspended   = "Suspended"
)

// Nix workload condition types (in addition to the shared kstatus ones in
// conditions.go: Ready, Reconciling, Stalled, GitSynced).
const (
	// ConditionProgressing indicates the native workload is mid-rollout.
	ConditionProgressing = "Progressing"
)

// Managed label keys stamped on every generated pod template.
const (
	LabelWorkloadKind = "nio.homystack.com/workload-kind"
	LabelWorkloadName = "nio.homystack.com/workload-name"
	LabelRevision     = "nio.homystack.com/revision"
	LabelManagedBy    = "app.kubernetes.io/managed-by"

	// AnnotationRevision carries the composite revision hash on the pod template;
	// changing it triggers the native rolling update.
	AnnotationRevision = "nio.homystack.com/revision"

	// ManagedByValue is the value for LabelManagedBy on NIO-owned objects.
	ManagedByValue = "nio"

	// WorkloadFinalizer is set on every NIO workload object.
	WorkloadFinalizer = "nio.homystack.com/finalizer"
)
