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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// recordingGit is a GitResolver that captures the credentials it was handed,
// so tests can assert the resolver wires CredentialsRef through to ls-remote.
type recordingGit struct {
	sha  string
	seen *gitauth.Creds
}

func (f *recordingGit) LsRemote(_ context.Context, _, _ string, creds *gitauth.Creds) (string, error) {
	f.seen = creds
	return f.sha, nil
}

func TestResolveRevisionWiresUsernamePassword(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "gitcreds"},
		Data: map[string][]byte{
			// Trailing newlines mimic file-sourced Secret values; they must be trimmed.
			"username": []byte("bob\n"),
			"password": []byte("s3cret\n"),
		},
	}
	c := fake.NewClientBuilder().WithRuntimeObjects(secret).Build()
	rec := &recordingGit{sha: "sha1"}
	res, err := resolveRevision(context.Background(), c, rec, "apps",
		niov1alpha1.NixSource{
			GitRepo:        "https://example.com/r",
			Ref:            "main",
			CredentialsRef: &niov1alpha1.SecretReference{Name: "gitcreds"},
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.revision != "sha1" {
		t.Errorf("revision = %q, want sha1", res.revision)
	}
	if rec.seen == nil {
		t.Fatal("credentials were not wired to LsRemote (got nil)")
	}
	if rec.seen.Username != "bob" || rec.seen.Password != "s3cret" {
		t.Errorf("creds = %+v, want trimmed bob/s3cret", rec.seen)
	}
}

func TestResolveRevisionWiresTokenAndSSHKey(t *testing.T) {
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "token"},
		Data:       map[string][]byte{"token": []byte("ghp_abc\n")},
	}
	sshSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "sshkey"},
		Data: map[string][]byte{
			"ssh-privatekey": []byte("PRIVATE"),
			"known_hosts":    []byte("host ssh-ed25519 AAAA"),
		},
	}
	c := fake.NewClientBuilder().WithRuntimeObjects(tokenSecret, sshSecret).Build()

	// Token-only secret populates the password (git-over-HTTPS token auth).
	rec := &recordingGit{sha: "s"}
	if _, err := resolveRevision(context.Background(), c, rec, "apps",
		niov1alpha1.NixSource{GitRepo: "https://x/r", Ref: "main",
			CredentialsRef: &niov1alpha1.SecretReference{Name: "token"}}); err != nil {
		t.Fatalf("token: %v", err)
	}
	if rec.seen == nil || rec.seen.Password != "ghp_abc" {
		t.Errorf("token not wired as password: %+v", rec.seen)
	}

	// SSH key + known_hosts are forwarded byte-exact.
	rec2 := &recordingGit{sha: "s"}
	if _, err := resolveRevision(context.Background(), c, rec2, "apps",
		niov1alpha1.NixSource{GitRepo: "git@x:r.git", Ref: "main",
			CredentialsRef: &niov1alpha1.SecretReference{Name: "sshkey"}}); err != nil {
		t.Fatalf("ssh: %v", err)
	}
	if rec2.seen == nil || string(rec2.seen.SSHKey) != "PRIVATE" ||
		string(rec2.seen.KnownHosts) != "host ssh-ed25519 AAAA" {
		t.Errorf("ssh key/known_hosts not wired: %+v", rec2.seen)
	}
}

func TestResolveRevisionNoCredentialsRefPassesNil(t *testing.T) {
	// Without a CredentialsRef the resolver must not touch the client and must
	// pass nil creds (public-repo path). A nil client proves it is untouched.
	rec := &recordingGit{sha: "s"}
	if _, err := resolveRevision(context.Background(), nil, rec, "apps",
		niov1alpha1.NixSource{GitRepo: "https://x/r", Ref: "main"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.seen != nil {
		t.Errorf("expected nil creds for public repo, got %+v", rec.seen)
	}
}

func TestResolveRevisionCredentialsSecretMissing(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	_, err := resolveRevision(context.Background(), c, &recordingGit{sha: "s"}, "apps",
		niov1alpha1.NixSource{GitRepo: "https://x/r", Ref: "main",
			CredentialsRef: &niov1alpha1.SecretReference{Name: "missing"}})
	if err == nil {
		t.Error("expected error when credentialsRef points at a missing secret")
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
