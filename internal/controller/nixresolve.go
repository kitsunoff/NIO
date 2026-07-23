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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

// GitCreds carries optional authentication for git operations against a private
// repository. Either SSH (SSHKey, optional KnownHosts) or HTTPS basic/token
// (Username, Password) is used, selected from the repo URL scheme.
type GitCreds struct {
	Username   string
	Password   string
	SSHKey     []byte
	KnownHosts []byte
}

// defaultGitRef is the ref resolved when a source specifies none.
const defaultGitRef = "main"

// GitResolver resolves a mutable git ref to an immutable commit SHA without a
// full clone. The default implementation shells out to `git ls-remote`; tests
// substitute a fake.
type GitResolver interface {
	LsRemote(ctx context.Context, repo, ref string, creds *GitCreds) (string, error)
}

// ExecGitResolver runs `git ls-remote` on the host (the operator image ships
// git). It is the production GitResolver.
type ExecGitResolver struct{}

// LsRemote returns the commit SHA that repo's ref currently points to. When
// creds is non-nil, authentication is wired for the ls-remote invocation
// (SSH key via GIT_SSH_COMMAND, or HTTPS basic/token via a non-interactive
// GIT_ASKPASS helper so the secret never appears in argv).
func (ExecGitResolver) LsRemote(ctx context.Context, repo, ref string, creds *GitCreds) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("gitRepo is empty")
	}
	cmd := exec.CommandContext(ctx, "git", "ls-remote", repo, ref)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	if creds != nil {
		cleanup, env, err := creds.gitEnv(repo)
		if err != nil {
			return "", err
		}
		defer cleanup()
		cmd.Env = append(cmd.Env, env...)
	}

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s %s: %w", repo, ref, err)
	}
	return parseLsRemote(string(out), ref)
}

// isSSHRepo reports whether repo is an SSH-style git URL.
func isSSHRepo(repo string) bool {
	if strings.HasPrefix(repo, "ssh://") || strings.HasPrefix(repo, "git@") {
		return true
	}
	// scp-like syntax "host:path" with no http(s) scheme.
	return strings.Contains(repo, "@") && !strings.HasPrefix(repo, "http")
}

// gitEnv materializes the credentials into temp files and returns the env vars
// git needs to authenticate, plus a cleanup func. On error it cleans up itself
// and returns a no-op cleanup.
func (c *GitCreds) gitEnv(repo string) (func(), []string, error) {
	dir, err := os.MkdirTemp("", "nio-gitcreds-")
	if err != nil {
		return func() {}, nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	if isSSHRepo(repo) {
		if len(c.SSHKey) == 0 {
			cleanup()
			return func() {}, nil, fmt.Errorf("ssh repo %q requires an 'ssh-privatekey' in the credentials secret", repo)
		}
		keyPath := filepath.Join(dir, "id")
		if err := os.WriteFile(keyPath, c.SSHKey, 0o600); err != nil {
			cleanup()
			return func() {}, nil, fmt.Errorf("write ssh key: %w", err)
		}
		sshCmd := "ssh -i " + keyPath + " -o IdentitiesOnly=yes"
		if len(c.KnownHosts) > 0 {
			khPath := filepath.Join(dir, "known_hosts")
			if err := os.WriteFile(khPath, c.KnownHosts, 0o600); err != nil {
				cleanup()
				return func() {}, nil, fmt.Errorf("write known_hosts: %w", err)
			}
			sshCmd += " -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + khPath
		} else {
			sshCmd += " -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
		}
		return cleanup, []string{"GIT_SSH_COMMAND=" + sshCmd}, nil
	}

	// HTTPS: a tiny askpass script feeds username/password from env, keeping the
	// token out of the process argument list.
	askPath := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s' \"$GIT_ASKPASS_USER\" ;;\n*) printf '%s' \"$GIT_ASKPASS_PASS\" ;;\nesac\n"
	if err := os.WriteFile(askPath, []byte(script), 0o700); err != nil {
		cleanup()
		return func() {}, nil, fmt.Errorf("write askpass helper: %w", err)
	}
	user := c.Username
	if user == "" {
		user = "git"
	}
	return cleanup, []string{
		"GIT_ASKPASS=" + askPath,
		"GIT_ASKPASS_USER=" + user,
		"GIT_ASKPASS_PASS=" + c.Password,
	}, nil
}

// parseLsRemote extracts the SHA for ref from `git ls-remote` output. It prefers
// an exact refs/heads/<ref> or refs/tags/<ref> match, falling back to the first
// line. Peeled tag lines ("^{}") win for annotated tags.
func parseLsRemote(out, ref string) (string, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("ref %q not found", ref)
	}
	var head, tag, peeled, first string
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sha, name := fields[0], fields[1]
		if first == "" {
			first = sha
		}
		switch name {
		case "refs/tags/" + ref + "^{}":
			peeled = sha
		case "refs/heads/" + ref:
			head = sha
		case "refs/tags/" + ref:
			tag = sha
		}
	}
	switch {
	case peeled != "":
		return peeled, nil
	case head != "":
		return head, nil
	case tag != "":
		return tag, nil
	case first != "":
		return first, nil
	}
	return "", fmt.Errorf("ref %q not found in ls-remote output", ref)
}

// resolvedSource is the outcome of resolving a NixSource: the immutable commit
// and, in Flux mode, the artifact tarball URL the pod's fetch-source downloads.
type resolvedSource struct {
	revision    string
	artifactURL string // non-empty only in Flux mode
}

// resolveRevision turns a NixSource into an immutable revision, in priority
// order: pinned Rev > Flux source status.artifact > git ls-remote(Ref).
func resolveRevision(ctx context.Context, c client.Client, git GitResolver, namespace string, src niov1alpha1.NixSource) (resolvedSource, error) {
	if src.Rev != "" {
		return resolvedSource{revision: src.Rev}, nil
	}
	if src.FluxSourceRef != nil {
		return resolveFluxArtifact(ctx, c, namespace, src.FluxSourceRef)
	}
	ref := src.Ref
	if ref == "" {
		ref = defaultGitRef
	}
	// NOTE: the workload family does not yet feed CredentialsRef to the
	// resolver; private-repo revision resolution for workloads is tracked
	// separately. NixosConfiguration wires credentials in its own resolver call.
	sha, err := git.LsRemote(ctx, src.GitRepo, ref, nil)
	if err != nil {
		return resolvedSource{}, err
	}
	return resolvedSource{revision: sha}, nil
}

// fluxSourceGroup is the Flux source API group.
const fluxSourceGroup = "source.toolkit.fluxcd.io"

// defaultFluxAPIVersion is used when FluxSourceRef.APIVersion is unset.
const defaultFluxAPIVersion = fluxSourceGroup + "/v1"

// resolveFluxArtifact reads status.artifact.{revision,url} from a Flux source
// object (GitRepository / OCIRepository / Bucket — same artifact contract).
func resolveFluxArtifact(ctx context.Context, c client.Client, namespace string, ref *niov1alpha1.FluxSourceRef) (resolvedSource, error) {
	apiVersion := ref.APIVersion
	if apiVersion == "" {
		apiVersion = defaultFluxAPIVersion
	}
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(ref.Kind)
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, obj); err != nil {
		return resolvedSource{}, fmt.Errorf("reading flux source %s/%s: %w", ref.Kind, ref.Name, err)
	}
	revision, found, err := unstructured.NestedString(obj.Object, "status", "artifact", "revision")
	if err != nil || !found || revision == "" {
		return resolvedSource{}, fmt.Errorf("flux source %s/%s has no status.artifact.revision yet", ref.Kind, ref.Name)
	}
	url, found, err := unstructured.NestedString(obj.Object, "status", "artifact", "url")
	if err != nil || !found || url == "" {
		return resolvedSource{}, fmt.Errorf("flux source %s/%s has no status.artifact.url yet", ref.Kind, ref.Name)
	}
	// Flux revisions look like "main@sha1:abcdef..." or "sha256:...": keep the
	// full string as the rollout key (it is opaque and stable per artifact).
	return resolvedSource{revision: normalizeFluxRevision(revision), artifactURL: url}, nil
}

// normalizeFluxRevision extracts the digest portion of a Flux revision string
// so the rollout key is a stable, compact commit-like token.
func normalizeFluxRevision(rev string) string {
	// Formats: "<branch>@sha1:<sha>", "<tag>@sha256:<digest>", or bare "<sha>".
	if at := strings.LastIndex(rev, ":"); at != -1 {
		return rev[at+1:]
	}
	return rev
}
