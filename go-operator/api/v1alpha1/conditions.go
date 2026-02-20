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

// Standard condition types for kstatus compliance.
const (
	// ConditionReady indicates the resource has reached a fully reconciled state.
	ConditionReady = "Ready"

	// ConditionReconciling indicates the controller is actively processing changes.
	ConditionReconciling = "Reconciling"

	// ConditionStalled indicates the controller cannot make progress.
	ConditionStalled = "Stalled"
)

// Machine-specific condition types.
const (
	// ConditionDiscoverable indicates SSH connectivity to the machine.
	ConditionDiscoverable = "Discoverable"

	// ConditionHardwareScanned indicates hardware facts were collected.
	ConditionHardwareScanned = "HardwareScanned"
)

// NixosConfiguration-specific condition types.
const (
	// ConditionApplied indicates configuration was applied to the machine.
	ConditionApplied = "Applied"

	// ConditionGitSynced indicates git repository was successfully cloned.
	ConditionGitSynced = "GitSynced"
)

// Generic reasons.
const (
	ReasonSucceeded   = "Succeeded"
	ReasonFailed      = "Failed"
	ReasonProgressing = "Progressing"
	ReasonWaiting     = "Waiting"
)

// Machine-specific reasons.
const (
	ReasonSSHConnected          = "SSHConnected"
	ReasonSSHFailed             = "SSHFailed"
	ReasonCredentialsMissing    = "CredentialsMissing"
	ReasonHardwareScanSucceeded = "HardwareScanSucceeded"
	ReasonHardwareScanFailed    = "HardwareScanFailed"
)

// NixosConfiguration-specific reasons.
const (
	ReasonConfigApplied     = "ConfigurationApplied"
	ReasonConfigRemoved     = "ConfigurationRemoved"
	ReasonApplyFailed       = "ApplyFailed"
	ReasonGitCloneSucceeded = "GitCloneSucceeded"
	ReasonGitCloneFailed    = "GitCloneFailed"
	ReasonMachineNotReady   = "MachineNotReady"
	ReasonMachineInUse      = "MachineInUse"
	ReasonQueued            = "Queued"
	ReasonApplyStarted      = "ApplyStarted"
	ReasonApplyInProgress   = "ApplyInProgress"
	ReasonJobPending        = "JobPending"
	ReasonDeadlineExceeded  = "DeadlineExceeded"
	ReasonOperationFailed   = "OperationFailed"
)

// Finalizer name for cleanup operations.
const FinalizerName = "nio.homystack.com/finalizer"
