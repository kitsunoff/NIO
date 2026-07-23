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

package controller

import (
	"context"
	"encoding/json"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
	"github.com/kitsunoff/nixos-operator/internal/applyjob"
)

// testResolvedSHA is a stand-in immutable commit SHA used across these tests.
const testResolvedSHA = "deadbeefsha"

func revTestConfig() *niov1alpha1.NixosConfiguration {
	return &niov1alpha1.NixosConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: niov1alpha1.NixosConfigurationSpec{
			MachineRef: niov1alpha1.MachineReference{Name: "node-01"},
			GitRepo:    "https://github.com/example/nixos.git",
			Ref:        "main",
			Flake:      "#web",
		},
	}
}

// TestNeedsApply_DetectsNewCommitOnSameRef guards the core drift fix: a new
// commit pushed to the same branch (ref name unchanged, config hash unchanged)
// must still trigger an apply because the resolved SHA differs from the last
// applied commit.
func TestNeedsApply_DetectsNewCommitOnSameRef(t *testing.T) {
	r := newApplyJobReconciler(t)
	config := revTestConfig()
	config.Status.AppliedCommit = "oldsha"
	config.Status.ConfigurationHash = r.calculateConfigHash(config)
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Status:     niov1alpha1.MachineStatus{AppliedConfiguration: "web"},
	}

	// New SHA on the same ref -> must apply.
	if apply, reason := r.needsApply(context.Background(), config, machine, "newsha"); !apply {
		t.Errorf("needsApply = false, want true for a new commit; reason=%q", reason)
	}

	// Same SHA -> nothing to do.
	if apply, reason := r.needsApply(context.Background(), config, machine, "oldsha"); apply {
		t.Errorf("needsApply = true (reason=%q), want false when SHA is unchanged", reason)
	}
}

// TestCreateApplyJob_AnnotatesResolvedRevision guards that the resolved SHA is
// recorded on the Job so success handling can persist the SHA, not the ref.
func TestCreateApplyJob_AnnotatesResolvedRevision(t *testing.T) {
	r := newApplyJobReconciler(t)
	config := revTestConfig()
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
	}

	job, err := r.createApplyJob(context.Background(), config, machine, testResolvedSHA)
	if err != nil {
		t.Fatalf("createApplyJob: %v", err)
	}
	if got := job.Annotations[AnnotationResolvedRevision]; got != testResolvedSHA {
		t.Errorf("annotation %s = %q, want %q", AnnotationResolvedRevision, got, testResolvedSHA)
	}
	// The ref name (not the SHA) still drives the shallow clone.
	if got := envToMap(job.Spec.Template.Spec.Containers[0].Env)["NIO_GIT_REF"]; got != "main" {
		t.Errorf("NIO_GIT_REF = %q, want the ref name %q", got, "main")
	}
}

// TestCreateApplyJob_DeliversInlineAdditionalFiles guards that declared inline
// additional files actually reach the apply Job via NIO_ADDITIONAL_FILES,
// rather than being silently dropped.
func TestCreateApplyJob_DeliversInlineAdditionalFiles(t *testing.T) {
	r := newApplyJobReconciler(t)
	config := revTestConfig()
	config.Spec.AdditionalFiles = []niov1alpha1.AdditionalFile{
		{
			Path:      "hardware/node-01.nix",
			ValueType: niov1alpha1.AdditionalFileValueTypeInline,
			Inline:    "{ hardware.enableAllFirmware = true; }",
		},
	}
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
	}

	job, err := r.createApplyJob(context.Background(), config, machine, "")
	if err != nil {
		t.Fatalf("createApplyJob: %v", err)
	}

	raw, ok := envToMap(job.Spec.Template.Spec.Containers[0].Env)["NIO_ADDITIONAL_FILES"]
	if !ok || raw == "" {
		t.Fatalf("NIO_ADDITIONAL_FILES not set; additional files were dropped")
	}
	var files []applyjob.AdditionalFile
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		t.Fatalf("NIO_ADDITIONAL_FILES is not valid JSON for the apply binary: %v", err)
	}
	if len(files) != 1 || files[0].Path != "hardware/node-01.nix" ||
		files[0].Content != "{ hardware.enableAllFirmware = true; }" {
		t.Errorf("delivered files = %+v, want the single inline file", files)
	}
}

// TestCreateApplyJob_UnsupportedAdditionalFileFailsLoudly guards that a value
// type the apply Job cannot yet deliver (SecretRef/NixosFacter) fails the apply
// instead of silently dropping the declared file.
func TestCreateApplyJob_UnsupportedAdditionalFileFailsLoudly(t *testing.T) {
	r := newApplyJobReconciler(t)
	config := revTestConfig()
	config.Spec.AdditionalFiles = []niov1alpha1.AdditionalFile{
		{
			Path:      "secrets/token",
			ValueType: niov1alpha1.AdditionalFileValueTypeSecretRef,
			SecretRef: &niov1alpha1.SecretKeyReference{Name: "s", Key: "k"},
		},
	}
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
	}

	if _, err := r.createApplyJob(context.Background(), config, machine, ""); err == nil {
		t.Error("createApplyJob succeeded for an unsupported additionalFile valueType, want error")
	}
}

// TestHandleJobSuccess_PersistsResolvedSHA guards that a successful apply
// records the immutable SHA from the Job annotation as the applied commit on
// both the config and the machine, not the ref name.
func TestHandleJobSuccess_PersistsResolvedSHA(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	config := revTestConfig()
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(machine).
		WithStatusSubresource(machine).
		Build()
	r := &NixosConfigurationReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "web-apply-1",
			Namespace:   "default",
			Annotations: map[string]string{AnnotationResolvedRevision: testResolvedSHA},
		},
		Status: batchv1.JobStatus{Succeeded: 1},
	}

	if _, err := r.handleJobSuccess(context.Background(), config, job, machine); err != nil {
		t.Fatalf("handleJobSuccess: %v", err)
	}
	if config.Status.AppliedCommit != testResolvedSHA {
		t.Errorf("config AppliedCommit = %q, want the resolved SHA", config.Status.AppliedCommit)
	}
	if machine.Status.AppliedCommit != testResolvedSHA {
		t.Errorf("machine AppliedCommit = %q, want the resolved SHA", machine.Status.AppliedCommit)
	}
}
