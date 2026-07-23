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
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
	"github.com/kitsunoff/nixos-operator/internal/gitauth"
)

func TestParseLsRemote(t *testing.T) {
	tests := []struct {
		name, out, ref, want string
	}{
		{
			name: "branch head",
			out:  "abc123def\trefs/heads/main\nzzz\trefs/heads/other\n",
			ref:  "main", want: "abc123def",
		},
		{
			name: "peeled annotated tag wins",
			out:  "aaa\trefs/tags/v1\nbbb\trefs/tags/v1^{}\n",
			ref:  "v1", want: "bbb",
		},
		{
			name: "lightweight tag",
			out:  "ccc\trefs/tags/v2\n",
			ref:  "v2", want: "ccc",
		},
		{
			name: "fallback first line",
			out:  "ddd\tHEAD\n",
			ref:  "main", want: "ddd",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLsRemote(tt.out, tt.ref)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseLsRemote = %q, want %q", got, tt.want)
			}
		})
	}

	if _, err := parseLsRemote("", "main"); err == nil {
		t.Error("expected error on empty output")
	}
}

// fakeGit is a test GitResolver.
type fakeGit struct {
	sha string
	err error
}

func (f fakeGit) LsRemote(_ context.Context, _, _ string, _ *gitauth.Creds) (string, error) {
	return f.sha, f.err
}

func TestResolveRevisionPinnedRev(t *testing.T) {
	// A pinned Rev must short-circuit without calling git.
	git := fakeGit{err: errors.New("git must not be called")}
	res, err := resolveRevision(context.Background(), nil, git, "default",
		niov1alpha1.NixSource{Rev: "cafebabe", GitRepo: "r", Ref: "main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.revision != "cafebabe" {
		t.Errorf("pinned Rev not honored: %q", res.revision)
	}
}

func TestResolveRevisionLsRemote(t *testing.T) {
	git := fakeGit{sha: "resolvedsha"}
	res, err := resolveRevision(context.Background(), nil, git, "default",
		niov1alpha1.NixSource{GitRepo: "https://example.com/r", Ref: "main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.revision != "resolvedsha" {
		t.Errorf("ls-remote revision = %q", res.revision)
	}
	if res.artifactURL != "" {
		t.Errorf("direct-git mode should not set artifactURL")
	}
}

func TestResolveRevisionFlux(t *testing.T) {
	src := &unstructured.Unstructured{}
	src.SetAPIVersion("source.toolkit.fluxcd.io/v1")
	src.SetKind("GitRepository")
	src.SetName("web")
	src.SetNamespace("apps")
	_ = unstructured.SetNestedMap(src.Object, map[string]any{
		"revision": "main@sha1:0123456789abcdef",
		"url":      "http://source-controller.flux-system.svc/g/apps/web/0123.tar.gz",
	}, "status", "artifact")

	scheme := fake.NewClientBuilder()
	scheme.WithRuntimeObjects(src)
	c := scheme.Build()

	res, err := resolveRevision(context.Background(), c, fakeGit{err: errors.New("git must not be called")}, "apps",
		niov1alpha1.NixSource{FluxSourceRef: &niov1alpha1.FluxSourceRef{Kind: "GitRepository", Name: "web"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.revision != "0123456789abcdef" {
		t.Errorf("flux revision = %q, want digest", res.revision)
	}
	if res.artifactURL == "" {
		t.Error("flux mode should set artifactURL")
	}
}

func TestResolveRevisionFluxMissingArtifact(t *testing.T) {
	src := &unstructured.Unstructured{}
	src.SetAPIVersion("source.toolkit.fluxcd.io/v1")
	src.SetKind("GitRepository")
	src.SetName("web")
	src.SetNamespace("apps")

	c := fake.NewClientBuilder().WithRuntimeObjects(src).Build()
	_, err := resolveRevision(context.Background(), c, fakeGit{}, "apps",
		niov1alpha1.NixSource{FluxSourceRef: &niov1alpha1.FluxSourceRef{Kind: "GitRepository", Name: "web"}})
	if err == nil {
		t.Error("expected error when Flux source has no artifact yet")
	}
}

func TestNormalizeFluxRevision(t *testing.T) {
	cases := map[string]string{
		"main@sha1:abcdef":   "abcdef",
		"v1.0.0@sha256:deed": "deed",
		"barehash":           "barehash",
	}
	for in, want := range cases {
		if got := normalizeFluxRevision(in); got != want {
			t.Errorf("normalizeFluxRevision(%q) = %q, want %q", in, got, want)
		}
	}
}
