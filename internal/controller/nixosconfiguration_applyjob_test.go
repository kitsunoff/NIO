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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

// envToMap flattens container env vars into a name->value map for assertions.
func envToMap(vars []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(vars))
	for _, e := range vars {
		m[e.Name] = e.Value
	}
	return m
}

func newApplyJobReconciler(t *testing.T) *NixosConfigurationReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return &NixosConfigurationReconciler{Scheme: scheme}
}

// TestCreateApplyJob_DispatchesToApplySubcommand guards the contract that lets
// the apply Job actually reach the apply logic: the container must invoke the
// "apply" subcommand, otherwise main.go falls through to running the manager
// and dies parsing unknown flags.
func TestCreateApplyJob_DispatchesToApplySubcommand(t *testing.T) {
	r := newApplyJobReconciler(t)

	config := &niov1alpha1.NixosConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: niov1alpha1.NixosConfigurationSpec{
			MachineRef:          niov1alpha1.MachineReference{Name: "node-01"},
			GitRepo:             "https://github.com/example/nixos.git",
			Ref:                 "main",
			Flake:               "#web",
			ConfigurationSubdir: "clusters/prod",
		},
	}
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec: niov1alpha1.MachineSpec{
			Host:            "10.0.0.5",
			SSHUser:         "root",
			SSHKeySecretRef: &niov1alpha1.SecretReference{Name: "node-01-ssh"},
		},
	}

	job, err := r.createApplyJob(context.Background(), config, machine)
	if err != nil {
		t.Fatalf("createApplyJob: %v", err)
	}

	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	c := containers[0]

	wantCmd := []string{"/manager", "apply"}
	if len(c.Command) != len(wantCmd) || c.Command[0] != wantCmd[0] || c.Command[1] != wantCmd[1] {
		t.Errorf("container Command = %v, want %v", c.Command, wantCmd)
	}

	// Legacy CLI flags (parsed by nobody) must not leak into the container.
	for _, a := range c.Args {
		if a == "--mode=apply-job" {
			t.Errorf("container Args still carries legacy flag %q", a)
		}
	}
}

// TestCreateApplyJob_PassesConfigViaEnv guards that the controller feeds the
// apply binary through the NIO_* environment contract that apply.LoadConfigFromEnv
// actually reads, and passes the SSH key as a mounted file path (not base64).
func TestCreateApplyJob_PassesConfigViaEnv(t *testing.T) {
	r := newApplyJobReconciler(t)

	config := &niov1alpha1.NixosConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: niov1alpha1.NixosConfigurationSpec{
			MachineRef:          niov1alpha1.MachineReference{Name: "node-01"},
			GitRepo:             "https://github.com/example/nixos.git",
			Ref:                 "main",
			Flake:               "#web",
			ConfigurationSubdir: "clusters/prod",
		},
	}
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec: niov1alpha1.MachineSpec{
			Host:            "10.0.0.5",
			SSHUser:         "root",
			SSHKeySecretRef: &niov1alpha1.SecretReference{Name: "node-01-ssh"},
		},
	}

	job, err := r.createApplyJob(context.Background(), config, machine)
	if err != nil {
		t.Fatalf("createApplyJob: %v", err)
	}

	env := envToMap(job.Spec.Template.Spec.Containers[0].Env)
	want := map[string]string{
		"NIO_GIT_REPO":      "https://github.com/example/nixos.git",
		"NIO_GIT_REF":       "main",
		"NIO_FLAKE":         "#web",
		"NIO_CONFIG_SUBDIR": "clusters/prod",
		"NIO_TARGET_HOST":   "10.0.0.5",
		"NIO_SSH_USER":      "root",
		"NIO_SSH_KEY_PATH":  "/secrets/ssh/ssh-privatekey",
		"NIO_OPERATION":     "NixosRebuild",
	}
	for k, v := range want {
		if got := env[k]; got != v {
			t.Errorf("env[%s] = %q, want %q", k, got, v)
		}
	}
}

// TestCreateApplyJob_FullInstallOperation guards that a full-disk install is
// signalled to the apply binary as the FullInstall operation.
func TestCreateApplyJob_FullInstallOperation(t *testing.T) {
	r := newApplyJobReconciler(t)

	config := &niov1alpha1.NixosConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: niov1alpha1.NixosConfigurationSpec{
			MachineRef:  niov1alpha1.MachineReference{Name: "node-01"},
			GitRepo:     "https://github.com/example/nixos.git",
			Ref:         "main",
			FullInstall: true,
		},
	}
	machine := &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "default"},
		Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.5", SSHUser: "root"},
	}

	job, err := r.createApplyJob(context.Background(), config, machine)
	if err != nil {
		t.Fatalf("createApplyJob: %v", err)
	}

	env := envToMap(job.Spec.Template.Spec.Containers[0].Env)
	if got := env["NIO_OPERATION"]; got != "FullInstall" {
		t.Errorf("env[NIO_OPERATION] = %q, want FullInstall", got)
	}
}
