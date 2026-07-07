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

	corev1 "k8s.io/api/core/v1"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

func TestCompositeRevisionStableAndSensitive(t *testing.T) {
	base := compositeRevision("abc123", ".#server", []string{"--port", "8080"})
	if base != compositeRevision("abc123", ".#server", []string{"--port", "8080"}) {
		t.Fatal("compositeRevision is not stable for identical inputs")
	}
	if !strings.HasPrefix(base, "r-") {
		t.Errorf("expected r- prefix, got %q", base)
	}

	cases := map[string][]any{
		"revision change": {"def456", ".#server", []string{"--port", "8080"}},
		"run change":      {"abc123", ".#other", []string{"--port", "8080"}},
		"args change":     {"abc123", ".#server", []string{"--port", "9090"}},
		"args count":      {"abc123", ".#server", []string{"--port"}},
	}
	for name, c := range cases {
		got := compositeRevision(c[0].(string), c[1].(string), c[2].([]string))
		if got == base {
			t.Errorf("%s: expected a different revision key, got same %q", name, got)
		}
	}

	// Field-boundary: ("a","b") must not collide with ("ab","").
	if compositeRevision("a", "b", nil) == compositeRevision("ab", "", nil) {
		t.Error("compositeRevision collides across field boundaries")
	}
}

func TestBuildNixConfig(t *testing.T) {
	store := &storeInfo{substituterURL: "http://store.apps.svc:5000", publicKey: "store:AbC=="}
	builder := &builderInfo{endpoint: "ssh-ng://root@b.apps.svc", systems: []string{"x86_64-linux"}}

	full := buildNixConfig(store, builder)
	if !strings.Contains(full, "http://store.apps.svc:5000") || !strings.Contains(full, cacheNixosURL) {
		t.Errorf("full config missing substituters: %q", full)
	}
	if !strings.Contains(full, "store:AbC==") || !strings.Contains(full, cacheNixosPublicKey) {
		t.Errorf("full config missing trusted keys: %q", full)
	}
	if !strings.Contains(full, "builders = ssh-ng://root@b.apps.svc x86_64-linux") {
		t.Errorf("full config missing builders line: %q", full)
	}
	if !strings.Contains(full, "builders-use-substitutes = true") {
		t.Errorf("full config missing builders-use-substitutes: %q", full)
	}

	ephemeral := buildNixConfig(nil, nil)
	if strings.Contains(ephemeral, "builders =") {
		t.Errorf("ephemeral config should have no builders line: %q", ephemeral)
	}
	if !strings.Contains(ephemeral, cacheNixosURL) {
		t.Errorf("ephemeral config should still trust cache.nixos.org: %q", ephemeral)
	}

	// Builder with no systems defaults to x86_64-linux.
	def := buildNixConfig(store, &builderInfo{endpoint: "ssh-ng://x"})
	if !strings.Contains(def, "ssh-ng://x "+defaultNixSystems) {
		t.Errorf("expected default system in builders line: %q", def)
	}
}

func TestRunAndBuildCommand(t *testing.T) {
	run := runCommand(".#server", []string{"--port", "8080"}, nil)
	want := []string{"nix", "run", ".#server", "--", "--port", "8080"}
	if strings.Join(run, " ") != strings.Join(want, " ") {
		t.Errorf("runCommand = %v, want %v", run, want)
	}

	noArgs := runCommand(".", nil, []string{"--option", "foo", "bar"})
	if strings.Contains(strings.Join(noArgs, " "), " -- ") {
		t.Errorf("runCommand with no args must not emit --: %v", noArgs)
	}
	if noArgs[2] != "--option" {
		t.Errorf("nix flags should precede the installable: %v", noArgs)
	}

	build := buildCommand(".#server", []string{".#dep"}, nil)
	want = []string{"nix", "build", ".#server", ".#dep"}
	if strings.Join(build, " ") != strings.Join(want, " ") {
		t.Errorf("buildCommand = %v, want %v", build, want)
	}
}

func TestFetchSourceScript(t *testing.T) {
	direct := fetchSourceScript(false)
	if !strings.Contains(direct, "git fetch --depth 1 origin") || !strings.Contains(direct, "$NIO_GIT_REPO") {
		t.Errorf("direct script missing git fetch: %q", direct)
	}
	flux := fetchSourceScript(true)
	if !strings.Contains(flux, "$NIO_ARTIFACT_URL") || !strings.Contains(flux, "tar --extract") {
		t.Errorf("flux script missing artifact download: %q", flux)
	}
}

func baseTemplateWithProbe() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/healthz"},
						},
					},
				},
			},
		},
	}
}

func TestRenderPodTemplateDirectGit(t *testing.T) {
	in := renderInput{
		spec: niov1alpha1.NixSpec{
			Source: niov1alpha1.NixSource{GitRepo: "https://github.com/acme/web", Ref: "main"},
			Run:    ".#server",
			Args:   []string{"--port", "8080"},
			Image:  "nixos/nix:2.24.0",
		},
		resolvedRevision: "abc123",
		store:            &storeInfo{substituterURL: "http://store.svc:5000", publicKey: "store:k=="},
		builder:          &builderInfo{endpoint: "ssh-ng://root@b.svc"},
		kind:             "NixDeployment",
		name:             "web",
	}
	out := renderPodTemplate(in, baseTemplateWithProbe())

	// Three init-containers in order.
	if len(out.Spec.InitContainers) != 3 {
		t.Fatalf("expected 3 init-containers, got %d", len(out.Spec.InitContainers))
	}
	order := []string{initBootstrap, initFetchSource, initInstantiate}
	for i, name := range order {
		if out.Spec.InitContainers[i].Name != name {
			t.Errorf("init-container %d = %q, want %q", i, out.Spec.InitContainers[i].Name, name)
		}
	}

	// fetch-source gets NIO_GIT_REPO + NIO_REVISION (direct mode).
	fetchEnv := envMap(out.Spec.InitContainers[1].Env)
	if fetchEnv["NIO_GIT_REPO"] != "https://github.com/acme/web" || fetchEnv["NIO_REVISION"] != "abc123" {
		t.Errorf("fetch-source env wrong: %v", fetchEnv)
	}

	// App container: image + command + NIX_CONFIG, probe preserved.
	var app *corev1.Container
	for i := range out.Spec.Containers {
		if out.Spec.Containers[i].Name == "app" {
			app = &out.Spec.Containers[i]
		}
	}
	if app == nil {
		t.Fatal("app container missing")
	}
	if app.Image != "nixos/nix:2.24.0" {
		t.Errorf("app image = %q", app.Image)
	}
	if strings.Join(app.Command, " ") != "nix run .#server -- --port 8080" {
		t.Errorf("app command = %v", app.Command)
	}
	if app.ReadinessProbe == nil {
		t.Error("user readinessProbe was dropped")
	}
	if envMap(app.Env)["NIX_CONFIG"] == "" {
		t.Error("app NIX_CONFIG not set")
	}

	// Volumes present.
	vols := map[string]bool{}
	for _, v := range out.Spec.Volumes {
		vols[v.Name] = true
	}
	if !vols[nixStorePodVolume] || !vols[workspaceVolume] {
		t.Errorf("missing volumes: %v", vols)
	}

	// Revision label + annotation set and equal.
	rev := compositeRevision("abc123", ".#server", []string{"--port", "8080"})
	if out.Labels[niov1alpha1.LabelRevision] != rev {
		t.Errorf("revision label = %q, want %q", out.Labels[niov1alpha1.LabelRevision], rev)
	}
	if out.Annotations[niov1alpha1.AnnotationRevision] != rev {
		t.Errorf("revision annotation = %q, want %q", out.Annotations[niov1alpha1.AnnotationRevision], rev)
	}
}

func TestRenderPodTemplateIdempotent(t *testing.T) {
	in := renderInput{
		spec:             niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{GitRepo: "r", Ref: "main"}, Run: "."},
		resolvedRevision: "abc",
		kind:             "NixJob",
		name:             "j",
	}
	once := renderPodTemplate(in, corev1.PodTemplateSpec{})
	twice := renderPodTemplate(in, once)
	if len(twice.Spec.InitContainers) != 3 {
		t.Errorf("re-render duplicated init-containers: got %d", len(twice.Spec.InitContainers))
	}
	appCount := 0
	for _, c := range twice.Spec.Containers {
		if c.Name == defaultAppContainer {
			appCount++
		}
	}
	if appCount != 1 {
		t.Errorf("re-render duplicated app container: got %d", appCount)
	}
}

func TestRenderPodTemplateFluxMode(t *testing.T) {
	in := renderInput{
		spec: niov1alpha1.NixSpec{
			Source: niov1alpha1.NixSource{FluxSourceRef: &niov1alpha1.FluxSourceRef{Kind: "GitRepository", Name: "web"}},
			Run:    ".",
		},
		resolvedRevision: "deadbeef",
		artifactURL:      "http://source-controller.flux-system.svc/gitrepository/apps/web/deadbeef.tar.gz",
		kind:             "NixDeployment",
		name:             "web",
	}
	out := renderPodTemplate(in, corev1.PodTemplateSpec{})
	fetchEnv := envMap(out.Spec.InitContainers[1].Env)
	if fetchEnv["NIO_ARTIFACT_URL"] == "" {
		t.Errorf("flux mode fetch-source missing NIO_ARTIFACT_URL: %v", fetchEnv)
	}
	if _, ok := fetchEnv["NIO_GIT_REPO"]; ok {
		t.Errorf("flux mode should not set NIO_GIT_REPO: %v", fetchEnv)
	}
}

func TestNixStoreVolumeMedia(t *testing.T) {
	if v := nixStoreVolume(nil); v.EmptyDir == nil {
		t.Error("default LocalStore should be emptyDir")
	}
	mem := nixStoreVolume(&niov1alpha1.NixLocalStore{Medium: "Memory"})
	if mem.EmptyDir == nil || mem.EmptyDir.Medium != corev1.StorageMediumMemory {
		t.Error("Memory medium should be tmpfs emptyDir")
	}
	pvc := nixStoreVolume(&niov1alpha1.NixLocalStore{Medium: "PodPVC", StorageClassName: "fast"})
	if pvc.Ephemeral == nil {
		t.Fatal("PodPVC medium should be an ephemeral volume")
	}
	if sc := pvc.Ephemeral.VolumeClaimTemplate.Spec.StorageClassName; sc == nil || *sc != "fast" {
		t.Errorf("PodPVC storageClassName not honored: %v", sc)
	}
}

func envMap(env []corev1.EnvVar) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		m[e.Name] = e.Value
	}
	return m
}

func TestRenderPodTemplateSSHWiring(t *testing.T) {
	base := niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{GitRepo: "r", Rev: "abc1234"}, Run: ".#x"}

	// Without a builder / ssh secret: no ssh volume, no NIX_SSHOPTS.
	out := renderPodTemplate(renderInput{spec: base, resolvedRevision: "abc1234", kind: "NixJob", name: "j"}, corev1.PodTemplateSpec{})
	for _, v := range out.Spec.Volumes {
		if v.Name == sshVolumeName {
			t.Fatal("ssh volume present without a builder")
		}
	}
	inst := containerByName(out.Spec.InitContainers, initInstantiate)
	if inst == nil || envMap(inst.Env)["NIX_SSHOPTS"] != "" {
		t.Error("NIX_SSHOPTS set on instantiate without a builder")
	}

	// With a builder + ssh secret: ssh volume mounted, NIX_SSHOPTS on instantiate+app.
	out = renderPodTemplate(renderInput{
		spec: base, resolvedRevision: "abc1234", kind: "NixJob", name: "j",
		builder:       &builderInfo{endpoint: "ssh-ng://root@b.svc"},
		sshSecretName: "store-ssh",
	}, corev1.PodTemplateSpec{})

	hasVol := false
	for _, v := range out.Spec.Volumes {
		if v.Name == sshVolumeName && v.Secret != nil && v.Secret.SecretName == "store-ssh" {
			hasVol = true
		}
	}
	if !hasVol {
		t.Error("ssh volume missing/misconfigured with a builder")
	}
	inst = containerByName(out.Spec.InitContainers, initInstantiate)
	if inst == nil || envMap(inst.Env)["NIX_SSHOPTS"] == "" {
		t.Error("NIX_SSHOPTS missing on instantiate with a builder")
	}
	mounted := false
	for _, m := range inst.VolumeMounts {
		if m.MountPath == sshKeyMountPath {
			mounted = true
		}
	}
	if !mounted {
		t.Error("ssh key not mounted into instantiate")
	}
}

// containerByName returns a pointer to the named container, or nil.
func containerByName(cs []corev1.Container, name string) *corev1.Container {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}
