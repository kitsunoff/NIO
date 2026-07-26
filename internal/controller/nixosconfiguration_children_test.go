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
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

func testConfig() *niov1alpha1.NixosConfiguration {
	inline := "{ networking.hostName = \"web\"; }"
	return &niov1alpha1.NixosConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "infra"},
		Spec: niov1alpha1.NixosConfigurationSpec{
			MachineRef:          niov1alpha1.MachineReference{Name: "web-machine"},
			GitRepo:             "https://github.com/acme/nixcfg",
			Ref:                 "main",
			Flake:               "#worker",
			OnRemoveFlake:       "#decommission",
			ConfigurationSubdir: "hosts/web",
			AdditionalFiles: []niov1alpha1.AdditionalFile{
				{Path: "local.nix", ValueType: niov1alpha1.AdditionalFileValueTypeInline, Inline: inline},
			},
		},
	}
}

func testMachine() *niov1alpha1.Machine {
	return &niov1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "web-machine", Namespace: "infra"},
		Spec: niov1alpha1.MachineSpec{
			Host:            "10.0.0.5",
			SSHUser:         "root",
			SSHKeySecretRef: &niov1alpha1.SecretReference{Name: "web-ssh"},
		},
	}
}

// podOf returns the injected pod template of a JobSpec.
func assertTargetSSH(t *testing.T, pod corev1.PodTemplateSpec) {
	t.Helper()
	var hasVol bool
	for _, v := range pod.Spec.Volumes {
		if v.Name == targetSSHVolumeName && v.Secret != nil && v.Secret.SecretName == "web-ssh" {
			hasVol = true
		}
	}
	if !hasVol {
		t.Errorf("target SSH key volume not injected: %+v", pod.Spec.Volumes)
	}
	if len(pod.Spec.Containers) == 0 || pod.Spec.Containers[0].Name != defaultAppContainer {
		t.Fatalf("expected an %q container carrying the injection", defaultAppContainer)
	}
	app := pod.Spec.Containers[0]
	var sshOpts string
	for _, e := range app.Env {
		if e.Name == "NIX_SSHOPTS" {
			sshOpts = e.Value
		}
	}
	if !strings.Contains(sshOpts, targetSSHKeyPath) {
		t.Errorf("NIX_SSHOPTS missing target key: %q", sshOpts)
	}
	var mounted bool
	for _, m := range app.VolumeMounts {
		if m.MountPath == targetSSHMountPath {
			mounted = true
		}
	}
	if !mounted {
		t.Error("target SSH key not mounted on app container")
	}
}

func TestBuildInstallNixJob(t *testing.T) {
	job, err := buildInstallNixJob(testConfig(), testMachine())
	if err != nil {
		t.Fatalf("buildInstallNixJob: %v", err)
	}
	if job.Name != "web-install" || job.Namespace != "infra" {
		t.Errorf("name/namespace = %s/%s", job.Name, job.Namespace)
	}
	nix := job.Spec.Nix
	if nix.Run != installerInstallable {
		t.Errorf("Run = %q, want nixos-anywhere", nix.Run)
	}
	// nixos-anywhere runs its own ssh (ssh-copy-id + control connection) and
	// ignores NIX_SSHOPTS, so the install child must pass the identity + permissive
	// host-key options explicitly via -i/--ssh-option, with the target host last.
	wantArgs := []string{
		"--flake", ".#worker",
		"-i", targetSSHKeyPath,
		"--ssh-option", "StrictHostKeyChecking=no",
		"--ssh-option", "UserKnownHostsFile=/dev/null",
		"root@10.0.0.5",
	}
	if strings.Join(nix.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf("Args = %v, want %v", nix.Args, wantArgs)
	}
	if nix.Source.Dir != "hosts/web" || nix.Source.GitRepo != "https://github.com/acme/nixcfg" {
		t.Errorf("Source wrong: %+v", nix.Source)
	}
	if len(nix.AdditionalFiles) != 1 || nix.AdditionalFiles[0].Inline == nil {
		t.Errorf("inline additionalFile not mapped: %+v", nix.AdditionalFiles)
	}
	if job.Spec.JobTemplate == nil {
		t.Fatal("JobTemplate nil")
	}
	assertTargetSSH(t, job.Spec.JobTemplate.Template)
}

func TestBuildDayTwoNixCronJob(t *testing.T) {
	cron, err := buildDayTwoNixCronJob(testConfig(), testMachine())
	if err != nil {
		t.Fatalf("buildDayTwoNixCronJob: %v", err)
	}
	if cron.Name != "web-day2" {
		t.Errorf("name = %q", cron.Name)
	}
	nix := cron.Spec.Nix
	if nix.Run != rebuildInstallable {
		t.Errorf("Run = %q, want nixos-rebuild", nix.Run)
	}
	if strings.Join(nix.Args, " ") != "switch --flake .#worker --target-host root@10.0.0.5" {
		t.Errorf("Args = %v", nix.Args)
	}
	if nix.TriggerOnChange == nil || !*nix.TriggerOnChange {
		t.Error("day-2 cron must set triggerOnChange=true")
	}
	ct := cron.Spec.CronJobTemplate
	if ct.Schedule != defaultDayTwoSchedule {
		t.Errorf("schedule = %q, want default", ct.Schedule)
	}
	if ct.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Errorf("concurrencyPolicy = %q, want Forbid (no overlapping nixos-rebuild)", ct.ConcurrencyPolicy)
	}
	assertTargetSSH(t, ct.JobTemplate.Spec.Template)
}

func TestBuildDayTwoNixCronJob_CustomSchedule(t *testing.T) {
	cfg := testConfig()
	cfg.Spec.DayTwoSchedule = "0 * * * *"
	cron, err := buildDayTwoNixCronJob(cfg, testMachine())
	if err != nil {
		t.Fatalf("buildDayTwoNixCronJob: %v", err)
	}
	if cron.Spec.CronJobTemplate.Schedule != "0 * * * *" {
		t.Errorf("custom schedule not honored: %q", cron.Spec.CronJobTemplate.Schedule)
	}
}

func TestBuildDecommissionNixJob(t *testing.T) {
	job, err := buildDecommissionNixJob(testConfig(), testMachine())
	if err != nil {
		t.Fatalf("buildDecommissionNixJob: %v", err)
	}
	if job.Name != "web-onremove" {
		t.Errorf("name = %q", job.Name)
	}
	if strings.Join(job.Spec.Nix.Args, " ") != "switch --flake .#decommission --target-host root@10.0.0.5" {
		t.Errorf("Args = %v (must use onRemoveFlake)", job.Spec.Nix.Args)
	}
}

// commitSHA is a full 40-char hex commit used to exercise the pinned-Rev path.
const commitSHA = "383f0f3489d4fe3828f4714a8c2f0de038946828"

// TestChildSource_PinnedSHARoutesToRev asserts that when the config's Ref is a
// full 40-hex commit SHA the child source pins it via Rev (which resolves without
// git, so it works in the distroless operator), leaving Ref empty. A branch ref
// keeps the current behavior (Ref set, Rev empty).
func TestChildSource_PinnedSHARoutesToRev(t *testing.T) {
	shaCfg := testConfig()
	shaCfg.Spec.Ref = commitSHA

	branchCfg := testConfig() // Ref == "main"

	install, err := buildInstallNixJob(shaCfg, testMachine())
	if err != nil {
		t.Fatalf("buildInstallNixJob: %v", err)
	}
	cron, err := buildDayTwoNixCronJob(shaCfg, testMachine())
	if err != nil {
		t.Fatalf("buildDayTwoNixCronJob: %v", err)
	}
	decom, err := buildDecommissionNixJob(shaCfg, testMachine())
	if err != nil {
		t.Fatalf("buildDecommissionNixJob: %v", err)
	}

	for _, tc := range []struct {
		name string
		src  niov1alpha1.NixSource
	}{
		{"install", install.Spec.Nix.Source},
		{"day2", cron.Spec.Nix.Source},
		{"decommission", decom.Spec.Nix.Source},
	} {
		if tc.src.Rev != commitSHA {
			t.Errorf("%s: Source.Rev = %q, want pinned SHA %q", tc.name, tc.src.Rev, commitSHA)
		}
		if tc.src.Ref != "" {
			t.Errorf("%s: Source.Ref = %q, want empty when SHA is pinned", tc.name, tc.src.Ref)
		}
	}

	binstall, err := buildInstallNixJob(branchCfg, testMachine())
	if err != nil {
		t.Fatalf("buildInstallNixJob (branch): %v", err)
	}
	if src := binstall.Spec.Nix.Source; src.Ref != "main" || src.Rev != "" {
		t.Errorf("branch ref: Source = {Ref:%q Rev:%q}, want {Ref:\"main\" Rev:\"\"}", src.Ref, src.Rev)
	}
}

// TestBuildChild_StoreBuilderRefPassthrough asserts the config's StoreRef and
// BuilderRef are passed straight through to all three children's NixSpec (opt-in
// build/substitute caching + build offload), and stay nil when the config leaves
// them unset.
func TestBuildChild_StoreBuilderRefPassthrough(t *testing.T) {
	cfg := testConfig()
	cfg.Spec.StoreRef = &niov1alpha1.LocalObjectReference{Name: "store"}
	cfg.Spec.BuilderRef = &niov1alpha1.LocalObjectReference{Name: "builder"}

	install, err := buildInstallNixJob(cfg, testMachine())
	if err != nil {
		t.Fatalf("buildInstallNixJob: %v", err)
	}
	cron, err := buildDayTwoNixCronJob(cfg, testMachine())
	if err != nil {
		t.Fatalf("buildDayTwoNixCronJob: %v", err)
	}
	decom, err := buildDecommissionNixJob(cfg, testMachine())
	if err != nil {
		t.Fatalf("buildDecommissionNixJob: %v", err)
	}

	for _, tc := range []struct {
		name string
		nix  niov1alpha1.NixSpec
	}{
		{"install", install.Spec.Nix},
		{"day2", cron.Spec.Nix},
		{"decommission", decom.Spec.Nix},
	} {
		if tc.nix.StoreRef == nil || tc.nix.StoreRef.Name != "store" {
			t.Errorf("%s: StoreRef = %+v, want {Name:\"store\"}", tc.name, tc.nix.StoreRef)
		}
		if tc.nix.BuilderRef == nil || tc.nix.BuilderRef.Name != "builder" {
			t.Errorf("%s: BuilderRef = %+v, want {Name:\"builder\"}", tc.name, tc.nix.BuilderRef)
		}
	}

	// Unset on the config → nil on every child.
	bare := testConfig() // StoreRef/BuilderRef nil
	binstall, err := buildInstallNixJob(bare, testMachine())
	if err != nil {
		t.Fatalf("buildInstallNixJob (bare): %v", err)
	}
	bcron, err := buildDayTwoNixCronJob(bare, testMachine())
	if err != nil {
		t.Fatalf("buildDayTwoNixCronJob (bare): %v", err)
	}
	bdecom, err := buildDecommissionNixJob(bare, testMachine())
	if err != nil {
		t.Fatalf("buildDecommissionNixJob (bare): %v", err)
	}
	for _, tc := range []struct {
		name string
		nix  niov1alpha1.NixSpec
	}{
		{"install", binstall.Spec.Nix},
		{"day2", bcron.Spec.Nix},
		{"decommission", bdecom.Spec.Nix},
	} {
		if tc.nix.StoreRef != nil {
			t.Errorf("%s: StoreRef = %+v, want nil when unset", tc.name, tc.nix.StoreRef)
		}
		if tc.nix.BuilderRef != nil {
			t.Errorf("%s: BuilderRef = %+v, want nil when unset", tc.name, tc.nix.BuilderRef)
		}
	}
}

func TestMapAdditionalFiles(t *testing.T) {
	cfg := &niov1alpha1.NixosConfiguration{Spec: niov1alpha1.NixosConfigurationSpec{
		AdditionalFiles: []niov1alpha1.AdditionalFile{
			{Path: "a.nix", ValueType: niov1alpha1.AdditionalFileValueTypeInline, Inline: "x"},
			{Path: "b.nix", ValueType: niov1alpha1.AdditionalFileValueTypeSecretRef,
				SecretRef: &niov1alpha1.SecretKeyReference{Name: "s", Key: "k"}},
		},
	}}
	files, err := mapAdditionalFiles(cfg)
	if err != nil {
		t.Fatalf("mapAdditionalFiles: %v", err)
	}
	if len(files) != 2 || files[0].Inline == nil || *files[0].Inline != "x" {
		t.Errorf("inline not mapped: %+v", files)
	}
	if files[1].SecretRef == nil || files[1].SecretRef.Key != "k" {
		t.Errorf("secretRef not mapped: %+v", files)
	}

	// NixosFacter is not supported → error.
	cfg.Spec.AdditionalFiles = []niov1alpha1.AdditionalFile{
		{Path: "c.nix", ValueType: niov1alpha1.AdditionalFileValueTypeNixosFacter, NixosFacter: true},
	}
	if _, err := mapAdditionalFiles(cfg); err == nil {
		t.Error("expected NixosFacter to be unsupported")
	}
}

func TestBuildChild_RequiresSSHKey(t *testing.T) {
	m := testMachine()
	m.Spec.SSHKeySecretRef = nil
	if _, err := buildInstallNixJob(testConfig(), m); err == nil {
		t.Error("expected error when machine has no sshKeySecretRef")
	}
	if _, err := buildDayTwoNixCronJob(testConfig(), m); err == nil {
		t.Error("expected error when machine has no sshKeySecretRef")
	}
}
