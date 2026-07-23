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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

// Pod-render constants for the generated NIO workload pods (design §4.5).
const (
	nixStorePodVolume = "nix-store"
	workspaceVolume   = "workspace"

	nixMountPath       = "/nix"
	nixBootstrapMount  = "/nix-vol"
	workspaceMountPath = "/workspace"

	initBootstrap   = "bootstrap"
	initFetchSource = "fetch-source"
	initInstantiate = "instantiate"

	defaultAppContainer = "app"
	// defaultNixSystems is what an unqualified NixBuilder advertises. It covers
	// both common Linux arches so the builder matches the runner pods' system
	// regardless of node architecture (the in-cluster builder is that arch).
	defaultNixSystems = "x86_64-linux,aarch64-linux"

	// cacheNixosPublicKey is the well-known public key for cache.nixos.org.
	cacheNixosPublicKey = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
	cacheNixosURL       = "https://cache.nixos.org"
)

// storeInfo carries the resolved NixStore endpoints for NIX_CONFIG assembly.
type storeInfo struct {
	substituterURL string
	publicKey      string
	pushURL        string // ssh-ng:// endpoint for pushing built paths into the store
}

// builderInfo carries the resolved NixBuilder endpoint for NIX_CONFIG assembly.
type builderInfo struct {
	endpoint string
	systems  []string
}

// compositeRevision returns the pod-template revision key
// hash(resolvedRevision + Run + Args), prefixed "r-". Changing any input rolls
// the workload (design §2.1, §4).
func compositeRevision(resolvedRevision, run string, args []string) string {
	h := sha256.New()
	// Length-prefixed fields so ("a","b") != ("ab",""), etc.
	writeField := func(s string) {
		_, _ = fmt.Fprintf(h, "%d:%s", len(s), s)
	}
	writeField(resolvedRevision)
	writeField(run)
	for _, a := range args {
		writeField(a)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return "r-" + sum[:12]
}

// buildNixConfig assembles the NIX_CONFIG value wiring nix to the NixStore
// (substituters + trusted-public-keys) and the NixBuilder (builders) when each
// is present. cache.nixos.org is always a trusted substituter fallback.
func buildNixConfig(store *storeInfo, builder *builderInfo) string {
	substituters := []string{}
	trustedKeys := []string{}
	if store != nil && store.substituterURL != "" {
		substituters = append(substituters, store.substituterURL)
		if store.publicKey != "" {
			trustedKeys = append(trustedKeys, store.publicKey)
		}
	}
	substituters = append(substituters, cacheNixosURL)
	trustedKeys = append(trustedKeys, cacheNixosPublicKey)

	lines := []string{
		"experimental-features = nix-command flakes",
		"substituters = " + strings.Join(substituters, " "),
		"trusted-public-keys = " + strings.Join(trustedKeys, " "),
	}
	if builder != nil && builder.endpoint != "" {
		systemList := defaultNixSystems
		if len(builder.systems) > 0 {
			systemList = strings.Join(builder.systems, ",")
		}
		lines = append(lines,
			"builders = "+builder.endpoint+" "+systemList,
			"builders-use-substitutes = true",
			// Force builds onto the remote builder: with a local job slot nix would
			// otherwise build locally and the builder→store push would never run.
			"max-jobs = 0",
		)
	}
	return strings.Join(lines, "\n")
}

// shellJoin single-quotes each argument and joins them for safe use inside an
// `sh -c` string (embedded single quotes are escaped as '\”).
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote single-quotes one argument for safe embedding in an `sh -c` string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runCommand builds the app container's `nix run <Run> -- <Args...>` command,
// with any extra nix flags before the installable.
func runCommand(run string, args, nixFlags []string) []string {
	cmd := []string{"nix", "run"}
	cmd = append(cmd, nixFlags...)
	cmd = append(cmd, run)
	if len(args) > 0 {
		cmd = append(cmd, "--")
		cmd = append(cmd, args...)
	}
	return cmd
}

// buildCommand builds the instantiate init's `nix build <Run> <Prebuild...>`.
func buildCommand(run string, prebuild, nixFlags []string) []string {
	cmd := []string{"nix", "build"}
	cmd = append(cmd, nixFlags...)
	cmd = append(cmd, run)
	cmd = append(cmd, prebuild...)
	return cmd
}

// fetchSourceScript returns the shell for the fetch-source init. Direct-git mode
// shallow-fetches the resolved commit; Flux mode downloads the artifact tarball
// and synthesizes a git tree so `.` is a hermetic flake input (design §4.5).
func fetchSourceScript(flux bool) string {
	if flux {
		return `set -eu
nix shell nixpkgs#gitMinimal nixpkgs#curl --command sh -c '
  curl --location --fail "$NIO_ARTIFACT_URL" | tar --extract --gzip --directory /workspace
  cd /workspace
  [ -e .git ] || (git init --quiet && git add --all --force && \
    git -c user.email=nio@homystack.com -c user.name=nio commit --quiet --message "flux artifact $NIO_REVISION")'
`
	}
	return `set -eu
nix shell nixpkgs#gitMinimal --command sh -c '
  git init --quiet /workspace && cd /workspace
  git remote add origin "$NIO_GIT_REPO"
  git fetch --depth 1 origin "$NIO_REVISION"
  git checkout --detach FETCH_HEAD'
`
}

// renderInput bundles everything needed to render a workload pod template.
type renderInput struct {
	spec             niov1alpha1.NixSpec
	resolvedRevision string
	artifactURL      string // set in Flux mode
	store            *storeInfo
	builder          *builderInfo
	sshSecretName    string // store-owned SSH key Secret; set only when a builder is used
	kind             string
	name             string
}

// renderPodTemplate stamps the operator-owned bits into the user's pod template:
// the three init-containers, the app container image/command/NIX_CONFIG, the
// nix-store + workspace volumes, and the managed labels + revision annotation.
// Everything the user provided (sidecars, probes, resources, scheduling) is
// preserved.
func renderPodTemplate(in renderInput, base corev1.PodTemplateSpec) corev1.PodTemplateSpec {
	tmpl := *base.DeepCopy()
	nix := in.spec
	image := nix.Image
	if image == "" {
		image = DefaultRunnerImage
	}
	appName := nix.ContainerName
	if appName == "" {
		appName = defaultAppContainer
	}
	rev := compositeRevision(in.resolvedRevision, nix.Run, nix.Args)
	nixConfig := buildNixConfig(in.store, in.builder)
	flux := nix.Source.FluxSourceRef != nil

	// Labels + revision annotation.
	labels := managedLabels(in.kind, in.name)
	labels[niov1alpha1.LabelRevision] = rev
	if tmpl.Labels == nil {
		tmpl.Labels = map[string]string{}
	}
	for k, v := range labels {
		tmpl.Labels[k] = v
	}
	if tmpl.Annotations == nil {
		tmpl.Annotations = map[string]string{}
	}
	tmpl.Annotations[niov1alpha1.AnnotationRevision] = rev

	// Volumes.
	tmpl.Spec.Volumes = upsertVolume(tmpl.Spec.Volumes, nixStoreVolume(nix.LocalStore))
	tmpl.Spec.Volumes = upsertVolume(tmpl.Spec.Volumes, corev1.Volume{
		Name:         workspaceVolume,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})

	nixAndWorkspace := []corev1.VolumeMount{
		{Name: nixStorePodVolume, MountPath: nixMountPath},
		{Name: workspaceVolume, MountPath: workspaceMountPath},
	}

	// When a builder is used, mount the store-owned SSH key so `nix build` can
	// dispatch to the builder over ssh-ng (builders= is already in NIX_CONFIG).
	buildMounts := nixAndWorkspace
	var sshOpts []corev1.EnvVar
	if in.sshSecretName != "" {
		mode := int32(0o400)
		tmpl.Spec.Volumes = upsertVolume(tmpl.Spec.Volumes, corev1.Volume{
			Name: sshVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: in.sshSecretName, DefaultMode: &mode},
			},
		})
		buildMounts = make([]corev1.VolumeMount, 0, len(nixAndWorkspace)+1)
		buildMounts = append(buildMounts, nixAndWorkspace...)
		buildMounts = append(buildMounts, corev1.VolumeMount{Name: sshVolumeName, MountPath: sshKeyMountPath, ReadOnly: true})
		sshOpts = []corev1.EnvVar{{
			Name:  "NIX_SSHOPTS",
			Value: "-i " + sshPrivateKeyPath + " -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null",
		}}
	}
	instantiateEnv := append([]corev1.EnvVar{{Name: "NIX_CONFIG", Value: nixConfig}}, sshOpts...)

	// When dispatching to a remote builder, nix invokes `ssh`; the nix image has
	// no ssh binary, so run the build/run commands inside a shell that brings
	// openssh onto PATH.
	wrapSSH := func(cmd []string) []string {
		if in.sshSecretName == "" {
			return cmd
		}
		return []string{"sh", "-c", "exec nix shell nixpkgs#openssh --command " + shellJoin(cmd)}
	}

	// instantiate: build (dispatched to the remote builder when one is used), and
	// with a store+builder also push the built closure into the shared NixStore so
	// other pods substitute it rather than rebuild (ADR-0008, delegated build).
	instantiateCmd := wrapSSH(buildCommand(nix.Run, nix.Prebuild, nix.NixFlags))
	if in.sshSecretName != "" && in.store != nil && in.store.pushURL != "" {
		installables := append([]string{nix.Run}, nix.Prebuild...)
		build := shellJoin(buildCommand(nix.Run, nix.Prebuild, nix.NixFlags))
		push := shellJoin(append([]string{"nix", "copy", "--to", in.store.pushURL}, installables...))
		instantiateCmd = []string{"sh", "-c", "exec nix shell nixpkgs#openssh --command sh -c " + shellQuote(build+" && "+push)}
	}

	// Init-containers (prepended, in order). fetch-source runs `nix shell
	// nixpkgs#gitMinimal`, so it needs NIX_CONFIG too (to enable nix-command and
	// to substitute git from the store/cache rather than build it).
	fetchEnv := []corev1.EnvVar{
		{Name: "NIX_CONFIG", Value: nixConfig},
		{Name: "NIO_REVISION", Value: in.resolvedRevision},
	}
	if flux {
		fetchEnv = append(fetchEnv, corev1.EnvVar{Name: "NIO_ARTIFACT_URL", Value: in.artifactURL})
	} else {
		fetchEnv = append(fetchEnv, corev1.EnvVar{Name: "NIO_GIT_REPO", Value: nix.Source.GitRepo})
	}

	inits := []corev1.Container{
		{
			Name:         initBootstrap,
			Image:        image,
			Command:      []string{"sh", "-c", "[ -e " + nixBootstrapMount + "/store ] || cp --archive /nix/. " + nixBootstrapMount + "/"},
			VolumeMounts: []corev1.VolumeMount{{Name: nixStorePodVolume, MountPath: nixBootstrapMount}},
		},
		{
			Name:         initFetchSource,
			Image:        image,
			Command:      []string{"sh", "-c", fetchSourceScript(flux)},
			Env:          fetchEnv,
			VolumeMounts: nixAndWorkspace,
		},
		{
			Name:         initInstantiate,
			Image:        image,
			WorkingDir:   workspaceMountPath,
			Command:      instantiateCmd,
			Env:          instantiateEnv,
			VolumeMounts: buildMounts,
		},
	}
	// Prepend our init-containers, dropping any prior copies (idempotent re-render).
	tmpl.Spec.InitContainers = append(inits, filterOutContainers(tmpl.Spec.InitContainers, initBootstrap, initFetchSource, initInstantiate)...)

	// App container: owned image/command/NIX_CONFIG/mounts, user fields preserved.
	app := findOrNewContainer(tmpl.Spec.Containers, appName)
	app.Image = image
	app.WorkingDir = workspaceMountPath
	app.Command = wrapSSH(runCommand(nix.Run, nix.Args, nix.NixFlags))
	app.Args = nil
	app.Env = upsertEnv(app.Env, corev1.EnvVar{Name: "NIX_CONFIG", Value: nixConfig})
	for _, e := range sshOpts {
		app.Env = upsertEnv(app.Env, e)
	}
	app.VolumeMounts = upsertMounts(app.VolumeMounts, buildMounts...)
	tmpl.Spec.Containers = setContainer(tmpl.Spec.Containers, app)

	return tmpl
}

// setContainer replaces the container with the same name verbatim, or appends
// it. Unlike upsertContainer, it does not field-merge — the caller has already
// composed the final container (used for the fully-rendered app container).
func setContainer(containers []corev1.Container, c corev1.Container) []corev1.Container {
	for i := range containers {
		if containers[i].Name == c.Name {
			containers[i] = c
			return containers
		}
	}
	return append(containers, c)
}

// nixStoreVolume builds the pod-local /nix volume per the LocalStore config.
func nixStoreVolume(ls *niov1alpha1.NixLocalStore) corev1.Volume {
	medium := "Disk"
	var sizeLimit *resource.Quantity
	if ls != nil {
		if ls.Medium != "" {
			medium = ls.Medium
		}
		sizeLimit = ls.SizeLimit
	}
	vol := corev1.Volume{Name: nixStorePodVolume}
	switch medium {
	case "Memory":
		vol.VolumeSource = corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			Medium: corev1.StorageMediumMemory, SizeLimit: sizeLimit,
		}}
	case "PodPVC":
		claim := &corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		}
		if ls != nil && ls.StorageClassName != "" {
			sc := ls.StorageClassName
			claim.StorageClassName = &sc
		}
		if sizeLimit != nil {
			claim.Resources = corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: *sizeLimit},
			}
		}
		vol.VolumeSource = corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{
			VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{Spec: *claim},
		}}
	default: // Disk
		vol.VolumeSource = corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: sizeLimit}}
	}
	return vol
}

// filterOutContainers returns containers with the named ones removed.
func filterOutContainers(containers []corev1.Container, names ...string) []corev1.Container {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	out := containers[:0:0]
	for _, c := range containers {
		if !drop[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// findOrNewContainer returns a copy of the named container, or a fresh one.
func findOrNewContainer(containers []corev1.Container, name string) corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return *containers[i].DeepCopy()
		}
	}
	return corev1.Container{Name: name}
}

// upsertEnv replaces an env var by name or appends it.
func upsertEnv(env []corev1.EnvVar, e corev1.EnvVar) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == e.Name {
			env[i] = e
			return env
		}
	}
	return append(env, e)
}

// upsertMounts adds mounts by mountPath, replacing existing ones at that path.
func upsertMounts(mounts []corev1.VolumeMount, add ...corev1.VolumeMount) []corev1.VolumeMount {
	byPath := map[string]int{}
	for i, m := range mounts {
		byPath[m.MountPath] = i
	}
	for _, m := range add {
		if i, ok := byPath[m.MountPath]; ok {
			mounts[i] = m
		} else {
			mounts = append(mounts, m)
		}
	}
	// Deterministic order for stable diffs.
	sort.SliceStable(mounts, func(i, j int) bool { return mounts[i].MountPath < mounts[j].MountPath })
	return mounts
}
