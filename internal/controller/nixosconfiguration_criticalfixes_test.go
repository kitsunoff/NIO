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
	"errors"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
	"github.com/kitsunoff/nixos-operator/internal/applyjob"
	"github.com/kitsunoff/nixos-operator/internal/gitauth"
)

// testResolvedSHA is a stand-in immutable commit SHA used across these tests.
const testResolvedSHA = "deadbeefsha"

// TestResolveConfigRevision exercises the GitResolver seam: success propagates
// the SHA, and an error propagates unchanged.
func TestResolveConfigRevision(t *testing.T) {
	r := newApplyJobReconciler(t)
	config := revTestConfig()

	r.Git = fakeGit{sha: testResolvedSHA}
	got, err := r.resolveConfigRevision(context.Background(), config)
	if err != nil {
		t.Fatalf("resolveConfigRevision: %v", err)
	}
	if got != testResolvedSHA {
		t.Errorf("resolveConfigRevision = %q, want %q", got, testResolvedSHA)
	}

	r.Git = fakeGit{err: errors.New("auth required")}
	if _, err := r.resolveConfigRevision(context.Background(), config); err == nil {
		t.Error("resolveConfigRevision returned nil error, want the resolver error propagated")
	}
}

// credCapturingGit records the credentials handed to the resolver.
type credCapturingGit struct {
	sha string
	got *gitauth.Creds
}

func (g *credCapturingGit) LsRemote(_ context.Context, _, _ string, creds *gitauth.Creds) (string, error) {
	g.got = creds
	return g.sha, nil
}

// TestResolveConfigRevision_PassesCredentialsFromSecret guards that a private
// repo's credentialsRef is read from the Secret and forwarded to the resolver
// so controller-side resolution can authenticate (variant B).
func TestResolveConfigRevision_PassesCredentialsFromSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}

	tests := []struct {
		name   string
		data   map[string][]byte
		verify func(t *testing.T, c *gitauth.Creds)
	}{
		{
			name: "https token",
			data: map[string][]byte{"username": []byte("git"), "password": []byte("ghp_token")},
			verify: func(t *testing.T, c *gitauth.Creds) {
				if c.Username != "git" || c.Password != "ghp_token" {
					t.Errorf("creds = %+v, want username=git password=ghp_token", c)
				}
			},
		},
		{
			name: "token only",
			data: map[string][]byte{"token": []byte("ghp_only")},
			verify: func(t *testing.T, c *gitauth.Creds) {
				if c.Password != "ghp_only" {
					t.Errorf("token not mapped to password: %+v", c)
				}
			},
		},
		{
			name: "trailing newline trimmed",
			data: map[string][]byte{"username": []byte("git\n"), "password": []byte("ghp_token\n")},
			verify: func(t *testing.T, c *gitauth.Creds) {
				// Must match cmd/apply.loadGitCreds trimming so controller-side
				// ls-remote and the Job clone authenticate identically.
				if c.Username != "git" || c.Password != "ghp_token" {
					t.Errorf("creds not trimmed: %+v", c)
				}
			},
		},
		{
			// Whitespace-only password + token must fall back to the token,
			// identically to cmd/apply.loadGitCreds (both gate on the trimmed
			// value). Otherwise controller and Job clone would diverge.
			name: "whitespace password falls back to token",
			data: map[string][]byte{"password": []byte("  \n"), "token": []byte("ghp_fallback")},
			verify: func(t *testing.T, c *gitauth.Creds) {
				if c.Password != "ghp_fallback" {
					t.Errorf("whitespace password did not fall back to token: %+v", c)
				}
			},
		},
		{
			name: "ssh key",
			data: map[string][]byte{"ssh-privatekey": []byte("PRIVKEY"), "known_hosts": []byte("host ssh-ed25519 AAA")},
			verify: func(t *testing.T, c *gitauth.Creds) {
				if string(c.SSHKey) != "PRIVKEY" || string(c.KnownHosts) != "host ssh-ed25519 AAA" {
					t.Errorf("ssh creds = %+v", c)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"},
				Data:       tt.data,
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
			cg := &credCapturingGit{sha: testResolvedSHA}
			r := &NixosConfigurationReconciler{Client: c, Scheme: scheme, Git: cg}

			config := revTestConfig()
			config.Spec.CredentialsRef = &niov1alpha1.SecretReference{Name: "git-creds"}

			got, err := r.resolveConfigRevision(context.Background(), config)
			if err != nil {
				t.Fatalf("resolveConfigRevision: %v", err)
			}
			if got != testResolvedSHA {
				t.Errorf("resolved = %q, want %q", got, testResolvedSHA)
			}
			if cg.got == nil {
				t.Fatal("resolver received nil credentials, want credentials from the secret")
			}
			tt.verify(t, cg.got)
		})
	}
}

// TestReconcile_ConvergedUsesGitPollInterval guards that a steady-state config
// requeues on the slower git-poll cadence, so it does not run `git ls-remote`
// every RequeueInterval.
func TestReconcile_ConvergedUsesGitPollInterval(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch scheme: %v", err)
	}

	const sha = "cafef00d"
	config := revTestConfig()
	config.Status.AppliedCommit = sha
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
		Status:     niov1alpha1.MachineStatus{Discoverable: true, AppliedConfiguration: config.Name},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(config, machine).
		WithStatusSubresource(config, machine).
		Build()
	r := &NixosConfigurationReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
		Git:      fakeGit{sha: sha},
	}
	// Make the config hash match so needsApply reports up to date.
	config.Status.ConfigurationHash = r.calculateConfigHash(config)
	if err := c.Status().Update(context.Background(), config); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: config.Name, Namespace: config.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != GitPollInterval {
		t.Errorf("RequeueAfter = %v, want GitPollInterval %v for a converged config", res.RequeueAfter, GitPollInterval)
	}
}

// TestCreateApplyJob_DefaultsEmptyRef guards that an empty spec.ref is defaulted
// to the same value resolution uses, so NIO_GIT_REF is never empty (which the
// apply binary rejects).
func TestCreateApplyJob_DefaultsEmptyRef(t *testing.T) {
	r := newApplyJobReconciler(t)
	config := revTestConfig()
	config.Spec.Ref = ""
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
	}

	job, err := r.createApplyJob(context.Background(), config, machine, "")
	if err != nil {
		t.Fatalf("createApplyJob: %v", err)
	}
	if got := envToMap(job.Spec.Template.Spec.Containers[0].Env)["NIO_GIT_REF"]; got != defaultGitRef {
		t.Errorf("NIO_GIT_REF = %q, want defaulted %q", got, defaultGitRef)
	}
}

// TestHandleJobSuccess_FallsBackToRefWithoutAnnotation guards the degraded /
// legacy path: a Job created without the resolved-revision annotation records
// the ref name as the applied commit rather than an empty string.
func TestHandleJobSuccess_FallsBackToRefWithoutAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	config := revTestConfig() // Spec.Ref == defaultGitRef
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
		ObjectMeta: metav1.ObjectMeta{Name: "web-apply-1", Namespace: "default"}, // no annotation
		Status:     batchv1.JobStatus{Succeeded: 1},
	}

	if _, err := r.handleJobSuccess(context.Background(), config, job, machine); err != nil {
		t.Fatalf("handleJobSuccess: %v", err)
	}
	if config.Status.AppliedCommit != defaultGitRef {
		t.Errorf("config AppliedCommit = %q, want fallback to ref %q", config.Status.AppliedCommit, defaultGitRef)
	}
	if machine.Status.AppliedCommit != defaultGitRef {
		t.Errorf("machine AppliedCommit = %q, want fallback to ref %q", machine.Status.AppliedCommit, defaultGitRef)
	}
}

// TestReconcile_GitResolveFailureDoesNotBlockApply guards the graceful
// degradation contract: when the controller cannot resolve the ref (bad
// credentials or a transient network failure), the first apply must still
// proceed rather than looping forever without a Job.
func TestReconcile_GitResolveFailureDoesNotBlockApply(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch scheme: %v", err)
	}

	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
		Status:     niov1alpha1.MachineStatus{Discoverable: true},
	}
	config := revTestConfig() // AppliedCommit == "" -> first apply
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(config, machine).
		WithStatusSubresource(config, machine).
		Build()
	r := &NixosConfigurationReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
		Git:      fakeGit{err: errors.New("authentication required")},
	}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: config.Name, Namespace: config.Namespace},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var jobs batchv1.JobList
	if err := c.List(context.Background(), &jobs); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 apply Job despite resolve failure, got %d", len(jobs.Items))
	}

	// The degraded state must be surfaced as GitSynced=False, not left stale.
	var updated niov1alpha1.NixosConfiguration
	if err := c.Get(context.Background(),
		types.NamespacedName{Name: config.Name, Namespace: config.Namespace}, &updated); err != nil {
		t.Fatalf("get config: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, niov1alpha1.ConditionGitSynced)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("GitSynced condition = %v, want present and False on degraded resolve", cond)
	}
}

func revTestConfig() *niov1alpha1.NixosConfiguration {
	return &niov1alpha1.NixosConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: niov1alpha1.NixosConfigurationSpec{
			MachineRef: niov1alpha1.MachineReference{Name: "node-01"},
			GitRepo:    "https://github.com/example/nixos.git",
			Ref:        defaultGitRef,
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

// TestCreateApplyJob_WritableWorkAndTmpDir guards that the apply Job is pointed
// at the writable /workspace emptyDir for both its work directory and Go's temp
// dir, since the container runs with a read-only root filesystem and would fail
// writing the clone / credential material to /tmp otherwise.
func TestCreateApplyJob_WritableWorkAndTmpDir(t *testing.T) {
	r := newApplyJobReconciler(t)
	config := revTestConfig()
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
	}

	job, err := r.createApplyJob(context.Background(), config, machine, "")
	if err != nil {
		t.Fatalf("createApplyJob: %v", err)
	}
	env := envToMap(job.Spec.Template.Spec.Containers[0].Env)
	if env["NIO_WORK_DIR"] != "/workspace" {
		t.Errorf("NIO_WORK_DIR = %q, want /workspace", env["NIO_WORK_DIR"])
	}
	if env["TMPDIR"] != "/workspace" {
		t.Errorf("TMPDIR = %q, want /workspace", env["TMPDIR"])
	}
}

// TestCreateApplyJob_MountsGitCredentialsPath guards that when a credentialsRef
// is set, the apply Job is told where the credentials Secret is mounted so its
// clone can authenticate to a private repo.
func TestCreateApplyJob_MountsGitCredentialsPath(t *testing.T) {
	r := newApplyJobReconciler(t)
	config := revTestConfig()
	config.Spec.CredentialsRef = &niov1alpha1.SecretReference{Name: "git-creds"}
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
	}

	job, err := r.createApplyJob(context.Background(), config, machine, "")
	if err != nil {
		t.Fatalf("createApplyJob: %v", err)
	}
	if got := envToMap(job.Spec.Template.Spec.Containers[0].Env)["NIO_GIT_CREDENTIALS_PATH"]; got != "/secrets/git" {
		t.Errorf("NIO_GIT_CREDENTIALS_PATH = %q, want /secrets/git", got)
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
	if got := envToMap(job.Spec.Template.Spec.Containers[0].Env)["NIO_GIT_REF"]; got != defaultGitRef {
		t.Errorf("NIO_GIT_REF = %q, want the ref name %q", got, defaultGitRef)
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
