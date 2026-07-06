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

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordSSHConnection_Success(t *testing.T) {
	// Reset counter
	SSHConnectionsTotal.Reset()

	RecordSSHConnection(true, 0.5)

	// Check counter incremented for success
	count := testutil.ToFloat64(SSHConnectionsTotal.WithLabelValues(ResultSuccess))
	if count != 1 {
		t.Errorf("expected success count 1, got %v", count)
	}

	failCount := testutil.ToFloat64(SSHConnectionsTotal.WithLabelValues(ResultFailure))
	if failCount != 0 {
		t.Errorf("expected failure count 0, got %v", failCount)
	}
}

func TestRecordSSHConnection_Failure(t *testing.T) {
	SSHConnectionsTotal.Reset()

	RecordSSHConnection(false, 1.0)

	count := testutil.ToFloat64(SSHConnectionsTotal.WithLabelValues(ResultFailure))
	if count != 1 {
		t.Errorf("expected failure count 1, got %v", count)
	}
}

func TestRecordGitClone_Success(t *testing.T) {
	GitClonesTotal.Reset()

	RecordGitClone(true, 5.0)

	count := testutil.ToFloat64(GitClonesTotal.WithLabelValues(ResultSuccess))
	if count != 1 {
		t.Errorf("expected success count 1, got %v", count)
	}
}

func TestRecordGitClone_Failure(t *testing.T) {
	GitClonesTotal.Reset()

	RecordGitClone(false, 2.0)

	count := testutil.ToFloat64(GitClonesTotal.WithLabelValues(ResultFailure))
	if count != 1 {
		t.Errorf("expected failure count 1, got %v", count)
	}
}

func TestRecordNixosBuild(t *testing.T) {
	NixosBuildsTotal.Reset()

	RecordNixosBuild("rebuild", true, 120.0)
	RecordNixosBuild("anywhere", false, 300.0)

	rebuildSuccess := testutil.ToFloat64(NixosBuildsTotal.WithLabelValues("rebuild", ResultSuccess))
	if rebuildSuccess != 1 {
		t.Errorf("expected rebuild success 1, got %v", rebuildSuccess)
	}

	anywhereFail := testutil.ToFloat64(NixosBuildsTotal.WithLabelValues("anywhere", ResultFailure))
	if anywhereFail != 1 {
		t.Errorf("expected anywhere failure 1, got %v", anywhereFail)
	}
}

func TestRecordJobCompletion(t *testing.T) {
	// Can't easily reset prometheus counters in tests
	// Just verify the functions don't panic and increment correctly
	initialFailed := testutil.ToFloat64(JobsFailedTotal)

	RecordJobCompletion("rebuild", true, 60.0)
	RecordJobCompletion("anywhere", false, 120.0)

	// Verify JobsFailedTotal incremented by 1 (for the failure case)
	afterFailed := testutil.ToFloat64(JobsFailedTotal)
	if afterFailed != initialFailed+1 {
		t.Errorf("expected JobsFailedTotal to increment by 1, got %v -> %v", initialFailed, afterFailed)
	}
}

func TestRecordError(t *testing.T) {
	ErrorsTotal.Reset()

	RecordError("ssh")
	RecordError("git")
	RecordError("ssh")

	sshCount := testutil.ToFloat64(ErrorsTotal.WithLabelValues("ssh"))
	if sshCount != 2 {
		t.Errorf("expected ssh error count 2, got %v", sshCount)
	}

	gitCount := testutil.ToFloat64(ErrorsTotal.WithLabelValues("git"))
	if gitCount != 1 {
		t.Errorf("expected git error count 1, got %v", gitCount)
	}
}

func TestUpdateMachineState(t *testing.T) {
	UpdateMachineState(10, 7, 5)

	total := testutil.ToFloat64(MachinesTotal)
	if total != 10 {
		t.Errorf("expected total 10, got %v", total)
	}

	discoverable := testutil.ToFloat64(MachinesDiscoverable)
	if discoverable != 7 {
		t.Errorf("expected discoverable 7, got %v", discoverable)
	}

	configured := testutil.ToFloat64(MachinesWithConfiguration)
	if configured != 5 {
		t.Errorf("expected configured 5, got %v", configured)
	}

	discoverableState := testutil.ToFloat64(MachinesByState.WithLabelValues("discoverable"))
	if discoverableState != 7 {
		t.Errorf("expected discoverable state 7, got %v", discoverableState)
	}

	undiscoverableState := testutil.ToFloat64(MachinesByState.WithLabelValues("undiscoverable"))
	if undiscoverableState != 3 {
		t.Errorf("expected undiscoverable state 3, got %v", undiscoverableState)
	}
}

func TestUpdateConfigState(t *testing.T) {
	UpdateConfigState(20, 5, 3, 10, 2)

	total := testutil.ToFloat64(ConfigurationsTotal)
	if total != 20 {
		t.Errorf("expected total 20, got %v", total)
	}

	pending := testutil.ToFloat64(ConfigsByState.WithLabelValues("pending"))
	if pending != 5 {
		t.Errorf("expected pending 5, got %v", pending)
	}

	applying := testutil.ToFloat64(ConfigsByState.WithLabelValues("applying"))
	if applying != 3 {
		t.Errorf("expected applying 3, got %v", applying)
	}

	applied := testutil.ToFloat64(ConfigsByState.WithLabelValues("applied"))
	if applied != 10 {
		t.Errorf("expected applied 10, got %v", applied)
	}

	failed := testutil.ToFloat64(ConfigsByState.WithLabelValues("failed"))
	if failed != 2 {
		t.Errorf("expected failed 2, got %v", failed)
	}
}

func TestMetricsLabels(t *testing.T) {
	// Verify that all label combinations work without panicking
	SSHConnectionsTotal.WithLabelValues(ResultSuccess)
	SSHConnectionsTotal.WithLabelValues(ResultFailure)

	GitClonesTotal.WithLabelValues(ResultSuccess)
	GitClonesTotal.WithLabelValues(ResultFailure)

	NixosBuildsTotal.WithLabelValues("rebuild", ResultSuccess)
	NixosBuildsTotal.WithLabelValues("rebuild", ResultFailure)
	NixosBuildsTotal.WithLabelValues("anywhere", ResultSuccess)
	NixosBuildsTotal.WithLabelValues("anywhere", ResultFailure)

	RetriesTotal.WithLabelValues("machine")
	RetriesTotal.WithLabelValues("configuration")

	ErrorsTotal.WithLabelValues("ssh")
	ErrorsTotal.WithLabelValues("git")
	ErrorsTotal.WithLabelValues("nix")
	ErrorsTotal.WithLabelValues("k8s")

	ReconcileDuration.WithLabelValues("machine", ResultSuccess)
	ReconcileDuration.WithLabelValues("configuration", ResultFailure)

	NixosBuildDuration.WithLabelValues("rebuild")
	NixosBuildDuration.WithLabelValues("anywhere")

	JobDuration.WithLabelValues("rebuild", ResultSuccess)
	JobDuration.WithLabelValues("anywhere", ResultFailure)

	MachinesByState.WithLabelValues("discoverable")
	MachinesByState.WithLabelValues("undiscoverable")

	ConfigsByState.WithLabelValues("pending")
	ConfigsByState.WithLabelValues("applying")
	ConfigsByState.WithLabelValues("applied")
	ConfigsByState.WithLabelValues("failed")
}
