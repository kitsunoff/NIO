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
	"path"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
	"github.com/kitsunoff/nixos-operator/internal/gitauth"
)

// Pod-render constants for the generated NIO workload pods (design §4.5).
const (
	nixStorePodVolume = "nix-store"
	workspaceVolume   = "workspace"

	nixMountPath       = "/nix"
	nixBootstrapMount  = "/nix-vol"
	workspaceMountPath = "/workspace"

	// gitCredsVolumeName / gitCredsMountPath expose a private-repo credentials
	// Secret to the fetch-source init-container (mirrors internal/gitauth keys).
	gitCredsVolumeName = "nio-git-creds"
	gitCredsMountPath  = "/etc/nio/git-creds"

	initBootstrap   = "bootstrap"
	initFetchSource = "fetch-source"
	initInjectFiles = "inject-files"
	initInstantiate = "instantiate"

	// filesMountBase is where referenced ConfigMap/Secret file sources are mounted
	// in the inject-files init-container (one indexed subdir per referenced object).
	filesMountBase = "/etc/nio/files"

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
// validateFilePath rejects an AdditionalFile destination that is absolute,
// escapes the source checkout root, or contains characters outside a safe set.
// The charset restriction also keeps the generated inject script's double-quoted
// paths free of shell metacharacters. Injected paths are attacker-influenced
// only by the CR author (who already controls the flake), but traversal into the
// pod filesystem is defense-in-depth worth enforcing.
func validateFilePath(p string) error {
	if p == "" {
		return fmt.Errorf("additionalFile path is empty")
	}
	if path.IsAbs(p) {
		return fmt.Errorf("additionalFile path %q must be relative", p)
	}
	for _, r := range p {
		if !isSafePathChar(r) {
			return fmt.Errorf("additionalFile path %q contains an unsupported character %q (allowed: letters, digits, . _ - /)", p, r)
		}
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("additionalFile path %q escapes the source tree", p)
	}
	return nil
}

// isSafePathChar reports whether r is allowed in an AdditionalFile path.
func isSafePathChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '.', r == '_', r == '-', r == '/':
		return true
	default:
		return false
	}
}

// validateAdditionalFiles checks every AdditionalFile destination path. It runs
// in the reconcile path so an invalid file stalls the workload instead of
// baking a bad path into the pod.
func validateAdditionalFiles(files []niov1alpha1.NixFile) error {
	for _, f := range files {
		if err := validateFilePath(f.Path); err != nil {
			return err
		}
	}
	return nil
}

// additionalFilesInjection builds the inject-files init-container (and the
// ConfigMap/Secret volumes it needs) that writes AdditionalFiles into the
// checkout and force-stages them, so a git-tree flake source includes them even
// under .gitignore. Every source is mounted and copied — explicit
// configMapRef/secretRef, and inline content via the operator-owned nixfiles
// ConfigMap (inlineCMName, key file-<index>) so inline never bloats the pod
// spec. Returns ok=false when there are no files.
func additionalFilesInjection(files []niov1alpha1.NixFile, image, inlineCMName string) (corev1.Container, []corev1.Volume, bool) {
	if len(files) == 0 {
		return corev1.Container{}, nil, false
	}
	mounts := []corev1.VolumeMount{
		{Name: nixStorePodVolume, MountPath: nixMountPath},
		{Name: workspaceVolume, MountPath: workspaceMountPath},
	}
	var vols []corev1.Volume
	volIdx := map[string]int{} // "cm/<name>" | "sec/<name>" -> index (dedup)
	mode := int32(0o400)
	refDir := func(kind, name string) string {
		key := kind + "/" + name
		if _, ok := volIdx[key]; !ok {
			idx := len(volIdx)
			volIdx[key] = idx
			var src corev1.VolumeSource
			if kind == "cm" {
				src = corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: name}, DefaultMode: &mode}}
			} else {
				src = corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name, DefaultMode: &mode}}
			}
			volName := fmt.Sprintf("nio-file-%d", idx)
			mountPath := fmt.Sprintf("%s/%d", filesMountBase, idx)
			vols = append(vols, corev1.Volume{Name: volName, VolumeSource: src})
			mounts = append(mounts, corev1.VolumeMount{Name: volName, MountPath: mountPath, ReadOnly: true})
		}
		return fmt.Sprintf("%s/%d", filesMountBase, volIdx[key])
	}

	var b strings.Builder
	b.WriteString("set -eu\ncd " + workspaceMountPath + "\n")
	paths := make([]string, 0, len(files))
	for i, f := range files {
		paths = append(paths, f.Path)
		// Paths are charset-validated, so double-quoting is metacharacter-safe.
		b.WriteString(`mkdir -p "$(dirname "` + f.Path + `")"` + "\n")
		switch {
		case f.ConfigMapRef != nil:
			b.WriteString(`cp "` + refDir("cm", f.ConfigMapRef.Name) + "/" + f.ConfigMapRef.Key + `" "` + f.Path + `"` + "\n")
		case f.SecretRef != nil:
			b.WriteString(`cp "` + refDir("sec", f.SecretRef.Name) + "/" + f.SecretRef.Key + `" "` + f.Path + `"` + "\n")
		default: // inline → copied from the owned nixfiles ConfigMap (out of the pod spec)
			src := refDir("cm", inlineCMName) + "/" + fmt.Sprintf("file-%d", i)
			b.WriteString(`cp "` + src + `" "` + f.Path + `"` + "\n")
		}
	}
	// Force-stage the exact paths (never `git add --all`, which honors .gitignore
	// and would silently drop matching files Nix then can't see).
	b.WriteString("git add --force --")
	for _, p := range paths {
		b.WriteString(` "` + p + `"`)
	}
	b.WriteByte('\n')

	return corev1.Container{
		Name:         initInjectFiles,
		Image:        image,
		Command:      []string{"nix", "shell", "nixpkgs#gitMinimal", "--command", "sh", "-c", b.String()},
		VolumeMounts: mounts,
	}, vols, true
}

// fetchSourceScript builds the fetch-source init-container script. In Flux mode
// it downloads the pre-authenticated artifact tarball. In direct-git mode it
// clones the exact resolved revision; when hasCreds is set it first wires
// private-repo auth from the mounted credentials (NIO_GIT_CREDENTIALS_PATH),
// mirroring the internal/gitauth posture — SSH via GIT_SSH_COMMAND (honoring a
// pinned known_hosts), HTTPS via a non-interactive GIT_ASKPASS helper so the
// secret never lands in argv.
func fetchSourceScript(flux, hasCreds, sshRepo bool) string {
	if flux {
		return `set -eu
nix shell nixpkgs#gitMinimal nixpkgs#curl --command sh -c '
  curl --location --fail "$NIO_ARTIFACT_URL" | tar --extract --gzip --directory /workspace
  cd /workspace
  [ -e .git ] || (git init --quiet && git add --all --force && \
    git -c user.email=nio@homystack.com -c user.name=nio commit --quiet --message "flux artifact $NIO_REVISION")'
`
	}

	pkgs := "nixpkgs#gitMinimal"
	authPrelude := ""
	switch {
	case hasCreds && sshRepo:
		// openssh is needed because git shells out to ssh, which the nix image lacks.
		pkgs = "nixpkgs#gitMinimal nixpkgs#openssh"
		// Copy the key to a writable path at 0600 (mounted secret files are
		// read-only and ssh rejects group/other-readable or ill-owned keys).
		authPrelude = `  install -m 600 "$NIO_GIT_CREDENTIALS_PATH/ssh-privatekey" /workspace/.nio-ssh-key
  if [ -s "$NIO_GIT_CREDENTIALS_PATH/known_hosts" ]; then
    GIT_SSH_COMMAND="ssh -i /workspace/.nio-ssh-key -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$NIO_GIT_CREDENTIALS_PATH/known_hosts"
  else
    GIT_SSH_COMMAND="ssh -i /workspace/.nio-ssh-key -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
  fi
  export GIT_SSH_COMMAND
`
	case hasCreds:
		// HTTPS: a GIT_ASKPASS helper reads username/password (or a token) from the
		// mounted secret at request time. Quoted heredoc → the helper body is written
		// verbatim (expanded when git invokes it, not now).
		authPrelude = `  cat > /workspace/.nio-askpass <<"NIOASKPASS"
#!/bin/sh
case "$1" in
Username*) head -n1 "$NIO_GIT_CREDENTIALS_PATH/username" 2>/dev/null || printf git ;;
Password*) head -n1 "$NIO_GIT_CREDENTIALS_PATH/password" 2>/dev/null || head -n1 "$NIO_GIT_CREDENTIALS_PATH/token" 2>/dev/null ;;
esac
NIOASKPASS
  chmod +x /workspace/.nio-askpass
  export GIT_ASKPASS=/workspace/.nio-askpass GIT_TERMINAL_PROMPT=0
`
	}

	return `set -eu
nix shell ` + pkgs + ` --command sh -c '
` + authPrelude + `  git init --quiet /workspace && cd /workspace
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

	// Private-repo credentials: mount the Secret into fetch-source and let the
	// clone authenticate. Credentials apply only to the direct-git clone (Flux
	// pulls a pre-authenticated artifact URL from the source-controller).
	fetchMounts := nixAndWorkspace
	credsSecretName := ""
	if !flux && nix.Source.CredentialsRef != nil {
		credsSecretName = nix.Source.CredentialsRef.Name
	}
	hasCreds := credsSecretName != ""
	sshRepo := hasCreds && gitauth.IsSSHRepo(nix.Source.GitRepo)
	if hasCreds {
		mode := int32(0o400)
		tmpl.Spec.Volumes = upsertVolume(tmpl.Spec.Volumes, corev1.Volume{
			Name: gitCredsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: credsSecretName, DefaultMode: &mode},
			},
		})
		fetchMounts = append(append([]corev1.VolumeMount{}, nixAndWorkspace...),
			corev1.VolumeMount{Name: gitCredsVolumeName, MountPath: gitCredsMountPath, ReadOnly: true})
		fetchEnv = append(fetchEnv, corev1.EnvVar{Name: "NIO_GIT_CREDENTIALS_PATH", Value: gitCredsMountPath})
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
			Command:      []string{"sh", "-c", fetchSourceScript(flux, hasCreds, sshRepo)},
			Env:          fetchEnv,
			VolumeMounts: fetchMounts,
		},
	}

	// inject-files (optional): write AdditionalFiles into the checkout and
	// force-stage them, after fetch-source's clone and before the build.
	if inject, injVols, ok := additionalFilesInjection(nix.AdditionalFiles, image, additionalFilesConfigMapName(in.name)); ok {
		for _, v := range injVols {
			tmpl.Spec.Volumes = upsertVolume(tmpl.Spec.Volumes, v)
		}
		inits = append(inits, inject)
	}

	inits = append(inits, corev1.Container{
		Name:         initInstantiate,
		Image:        image,
		WorkingDir:   workspaceMountPath,
		Command:      instantiateCmd,
		Env:          instantiateEnv,
		VolumeMounts: buildMounts,
	})
	// Prepend our init-containers, dropping any prior copies (idempotent re-render).
	tmpl.Spec.InitContainers = append(inits, filterOutContainers(tmpl.Spec.InitContainers, initBootstrap, initFetchSource, initInjectFiles, initInstantiate)...)

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
