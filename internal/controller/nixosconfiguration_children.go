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
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

// The v1alpha2 NixosConfiguration orchestrator applies nothing itself: it drives
// child NixJob/NixCronJob workloads. These builders turn a NixosConfiguration +
// its target Machine into those children — Source/subdir/additionalFiles from the
// config, and the target host's SSH key + NIX_SSHOPTS injected into the child pod
// so `nixos-rebuild`/`nixos-anywhere` can reach the host. The target details come
// entirely from the Machine (no NixTarget field).
const (
	targetSSHVolumeName = "nio-target-ssh"
	targetSSHMountPath  = "/etc/nio/target-ssh"
	targetSSHKeyPath    = targetSSHMountPath + "/ssh-privatekey"

	// targetNixSSHOpts is permissive by decision (no host-key pinning in
	// v1alpha2) — matches the builder-dispatch posture.
	targetNixSSHOpts = "-i " + targetSSHKeyPath +
		" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"

	installerInstallable = "github:nix-community/nixos-anywhere"
	rebuildInstallable   = "nixpkgs#nixos-rebuild"

	defaultDayTwoSchedule = "*/30 * * * *"
)

func installChildName(cfg string) string      { return cfg + "-install" }
func dayTwoChildName(cfg string) string       { return cfg + "-day2" }
func decommissionChildName(cfg string) string { return cfg + "-onremove" }

// mapAdditionalFiles converts the config's AdditionalFiles into the workload
// NixFile form. Inline and SecretRef map directly; NixosFacter is not supported.
func mapAdditionalFiles(config *niov1alpha1.NixosConfiguration) ([]niov1alpha1.NixFile, error) {
	if len(config.Spec.AdditionalFiles) == 0 {
		return nil, nil
	}
	out := make([]niov1alpha1.NixFile, 0, len(config.Spec.AdditionalFiles))
	for _, f := range config.Spec.AdditionalFiles {
		switch f.ValueType {
		case niov1alpha1.AdditionalFileValueTypeInline:
			inline := f.Inline
			out = append(out, niov1alpha1.NixFile{Path: f.Path, Inline: &inline})
		case niov1alpha1.AdditionalFileValueTypeSecretRef:
			out = append(out, niov1alpha1.NixFile{Path: f.Path, SecretRef: f.SecretRef})
		default:
			return nil, fmt.Errorf("additionalFile %q: valueType %q is not supported", f.Path, f.ValueType)
		}
	}
	return out, nil
}

// isFullCommitSHA reports whether s is a full 40-char hex commit SHA. Such a
// value must route to NixSource.Rev (resolved verbatim, no git) rather than Ref
// (resolved via `git ls-remote`, which the distroless operator image cannot run).
func isFullCommitSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// childNixSource is the flake source shared by every child. A full commit SHA in
// config.Spec.Ref is pinned via Rev (resolves without git, works in the
// distroless operator); a branch/tag ref stays on Ref (git ls-remote polling).
func childNixSource(config *niov1alpha1.NixosConfiguration) niov1alpha1.NixSource {
	src := niov1alpha1.NixSource{
		GitRepo:        config.Spec.GitRepo,
		CredentialsRef: config.Spec.CredentialsRef,
		Dir:            config.Spec.ConfigurationSubdir,
	}
	if isFullCommitSHA(config.Spec.Ref) {
		src.Rev = config.Spec.Ref
	} else {
		src.Ref = config.Spec.Ref
	}
	return src
}

// targetHost returns "<user>@<host>" for the machine (user defaults to root).
func targetHost(machine *niov1alpha1.Machine) string {
	user := machine.Spec.SSHUser
	if user == "" {
		user = "root"
	}
	return user + "@" + machine.Spec.Host
}

// flakeInstallable prefixes "." to the config's flake attr (e.g. "#worker" →
// ".#worker"), resolved against the checkout (subdir when Source.Dir is set).
func flakeInstallable(attr string) string {
	return "." + attr
}

// targetSSHPodTemplate injects the machine's SSH key + NIX_SSHOPTS onto the
// child's app container. nixrender preserves these (upsert) and, seeing
// NIX_SSHOPTS, wraps the app in openssh so it can reach the target host.
func targetSSHPodTemplate(machine *niov1alpha1.Machine) corev1.PodTemplateSpec {
	mode := int32(0o400)
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: targetSSHVolumeName,
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: machine.Spec.SSHKeySecretRef.Name, DefaultMode: &mode,
				}},
			}},
			Containers: []corev1.Container{{
				Name: defaultAppContainer,
				Env:  []corev1.EnvVar{{Name: "NIX_SSHOPTS", Value: targetNixSSHOpts}},
				VolumeMounts: []corev1.VolumeMount{{
					Name: targetSSHVolumeName, MountPath: targetSSHMountPath, ReadOnly: true,
				}},
			}},
		},
	}
}

// requireTargetKey guards that the machine can be reached over SSH with a key
// (the apply path is key-only, as in v1alpha1).
func requireTargetKey(machine *niov1alpha1.Machine) error {
	if machine.Spec.SSHKeySecretRef == nil {
		return fmt.Errorf("machine %q has no sshKeySecretRef; cannot apply over SSH", machine.Name)
	}
	return nil
}

// installAnywhereArgs builds the nixos-anywhere install args. nixos-anywhere runs
// its own ssh (ssh-copy-id + control connection) and does NOT honor NIX_SSHOPTS,
// so the identity file and permissive host-key options are passed explicitly via
// -i/--ssh-option; the target host stays the trailing positional arg.
func installAnywhereArgs(config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) []string {
	return []string{
		"--flake", flakeInstallable(config.Spec.Flake),
		"-i", targetSSHKeyPath,
		"--ssh-option", "StrictHostKeyChecking=no",
		"--ssh-option", "UserKnownHostsFile=/dev/null",
		targetHost(machine),
	}
}

// buildInstallNixJob builds the one-shot full-disk install child (nixos-anywhere).
func buildInstallNixJob(config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) (*niov1alpha1.NixJob, error) {
	if err := requireTargetKey(machine); err != nil {
		return nil, err
	}
	files, err := mapAdditionalFiles(config)
	if err != nil {
		return nil, err
	}
	pod := targetSSHPodTemplate(machine)
	return &niov1alpha1.NixJob{
		ObjectMeta: metav1.ObjectMeta{Name: installChildName(config.Name), Namespace: config.Namespace},
		Spec: niov1alpha1.NixJobSpec{
			Nix: niov1alpha1.NixSpec{
				Source:          childNixSource(config),
				Run:             installerInstallable,
				Args:            installAnywhereArgs(config, machine),
				AdditionalFiles: files,
			},
			JobTemplate: &batchv1.JobSpec{Template: pod},
		},
	}, nil
}

// buildDayTwoNixCronJob builds the recurring day-2 convergence child
// (nixos-rebuild switch), firing promptly on a new revision and serialized.
func buildDayTwoNixCronJob(config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) (*niov1alpha1.NixCronJob, error) {
	if err := requireTargetKey(machine); err != nil {
		return nil, err
	}
	files, err := mapAdditionalFiles(config)
	if err != nil {
		return nil, err
	}
	schedule := config.Spec.DayTwoSchedule
	if schedule == "" {
		schedule = defaultDayTwoSchedule
	}
	pod := targetSSHPodTemplate(machine)
	return &niov1alpha1.NixCronJob{
		ObjectMeta: metav1.ObjectMeta{Name: dayTwoChildName(config.Name), Namespace: config.Namespace},
		Spec: niov1alpha1.NixCronJobSpec{
			Nix: niov1alpha1.NixSpec{
				Source: childNixSource(config),
				Run:    rebuildInstallable,
				Args: []string{
					"switch", "--flake", flakeInstallable(config.Spec.Flake),
					"--target-host", targetHost(machine),
				},
				AdditionalFiles: files,
				TriggerOnChange: ptr(true),
			},
			CronJobTemplate: batchv1.CronJobSpec{
				Schedule:          schedule,
				ConcurrencyPolicy: batchv1.ForbidConcurrent,
				JobTemplate:       batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: pod}},
			},
		},
	}, nil
}

// buildDecommissionNixJob builds the one-shot decommission child applying
// onRemoveFlake. The orchestrator creates it WITHOUT an ownerRef (orphan) so it
// outlives the parent's deletion; this builder only shapes the object.
func buildDecommissionNixJob(config *niov1alpha1.NixosConfiguration, machine *niov1alpha1.Machine) (*niov1alpha1.NixJob, error) {
	if err := requireTargetKey(machine); err != nil {
		return nil, err
	}
	files, err := mapAdditionalFiles(config)
	if err != nil {
		return nil, err
	}
	pod := targetSSHPodTemplate(machine)
	return &niov1alpha1.NixJob{
		ObjectMeta: metav1.ObjectMeta{Name: decommissionChildName(config.Name), Namespace: config.Namespace},
		Spec: niov1alpha1.NixJobSpec{
			Nix: niov1alpha1.NixSpec{
				Source: childNixSource(config),
				Run:    rebuildInstallable,
				Args: []string{
					"switch", "--flake", flakeInstallable(config.Spec.OnRemoveFlake),
					"--target-host", targetHost(machine),
				},
				AdditionalFiles: files,
			},
			JobTemplate: &batchv1.JobSpec{Template: pod},
		},
	}, nil
}
