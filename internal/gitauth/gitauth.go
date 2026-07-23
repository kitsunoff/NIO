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

// Package gitauth builds the environment a git subprocess needs to authenticate
// against a private repository, without leaking secrets into the process
// argument list. It is shared by controller-side ref resolution (git ls-remote)
// and the apply Job's clone so both agree on authentication.
package gitauth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Creds carries optional authentication for git operations. Either SSH (SSHKey,
// optional KnownHosts) or HTTPS basic/token (Username, Password) is used,
// selected from the repository URL scheme.
type Creds struct {
	Username   string
	Password   string
	SSHKey     []byte
	KnownHosts []byte
}

// IsZero reports whether no credential material is present.
func (c *Creds) IsZero() bool {
	return c == nil || (c.Username == "" && c.Password == "" && len(c.SSHKey) == 0)
}

// IsSSHRepo reports whether repo is an SSH-style git URL.
func IsSSHRepo(repo string) bool {
	if strings.HasPrefix(repo, "ssh://") || strings.HasPrefix(repo, "git@") {
		return true
	}
	// scp-like syntax "host:path" with no http(s) scheme.
	return strings.Contains(repo, "@") && !strings.HasPrefix(repo, "http")
}

// Env materializes the credentials into temp files and returns the env vars git
// needs to authenticate plus a cleanup func. When there is nothing to set up it
// returns an empty slice and a no-op cleanup. On error it cleans up after
// itself and returns a no-op cleanup.
func (c *Creds) Env(repo string) (env []string, cleanup func(), err error) {
	noop := func() {}
	if c.IsZero() {
		return nil, noop, nil
	}

	dir, err := os.MkdirTemp("", "nio-gitauth-")
	if err != nil {
		return nil, noop, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	if IsSSHRepo(repo) {
		if len(c.SSHKey) == 0 {
			cleanup()
			return nil, noop, fmt.Errorf("ssh repo %q requires an 'ssh-privatekey' credential", repo)
		}
		keyPath := filepath.Join(dir, "id")
		if werr := os.WriteFile(keyPath, c.SSHKey, 0o600); werr != nil {
			cleanup()
			return nil, noop, fmt.Errorf("write ssh key: %w", werr)
		}
		sshCmd := "ssh -i " + keyPath + " -o IdentitiesOnly=yes"
		if len(c.KnownHosts) > 0 {
			khPath := filepath.Join(dir, "known_hosts")
			if werr := os.WriteFile(khPath, c.KnownHosts, 0o600); werr != nil {
				cleanup()
				return nil, noop, fmt.Errorf("write known_hosts: %w", werr)
			}
			sshCmd += " -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + khPath
		} else {
			sshCmd += " -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
		}
		return []string{"GIT_SSH_COMMAND=" + sshCmd}, cleanup, nil
	}

	// HTTPS: a tiny askpass script feeds username/password from env, keeping the
	// token out of the process argument list.
	askPath := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s' \"$GIT_ASKPASS_USER\" ;;\n*) printf '%s' \"$GIT_ASKPASS_PASS\" ;;\nesac\n"
	if werr := os.WriteFile(askPath, []byte(script), 0o700); werr != nil { //nolint:gosec // askpass helper must be executable
		cleanup()
		return nil, noop, fmt.Errorf("write askpass helper: %w", werr)
	}
	user := c.Username
	if user == "" {
		user = "git"
	}
	return []string{
		"GIT_ASKPASS=" + askPath,
		"GIT_ASKPASS_USER=" + user,
		"GIT_ASKPASS_PASS=" + c.Password,
	}, cleanup, nil
}
