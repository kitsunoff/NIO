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

package gitauth

import (
	"os"
	"strings"
	"testing"
)

func TestIsZero(t *testing.T) {
	var nilCreds *Creds
	if !nilCreds.IsZero() {
		t.Error("nil creds should be zero")
	}
	if !(&Creds{}).IsZero() {
		t.Error("empty creds should be zero")
	}
	if (&Creds{Password: "x"}).IsZero() {
		t.Error("creds with a password are not zero")
	}
	if (&Creds{SSHKey: []byte("k")}).IsZero() {
		t.Error("creds with an ssh key are not zero")
	}
}

func TestIsSSHRepo(t *testing.T) {
	tests := map[string]bool{
		"git@github.com:o/r.git":     true,
		"ssh://git@host/o/r":         true,
		"https://github.com/o/r.git": false,
		"http://github.com/o/r.git":  false,
	}
	for repo, want := range tests {
		if got := IsSSHRepo(repo); got != want {
			t.Errorf("IsSSHRepo(%q) = %v, want %v", repo, got, want)
		}
	}
}

func TestEnv_Zero(t *testing.T) {
	env, cleanup, err := (&Creds{}).Env("https://github.com/o/r.git")
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	defer cleanup()
	if len(env) != 0 {
		t.Errorf("zero creds produced env %v, want none", env)
	}
}

func TestEnv_HTTPS(t *testing.T) {
	creds := &Creds{Username: "u", Password: "p"}
	env, cleanup, err := creds.Env("https://github.com/o/r.git")
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	defer cleanup()

	m := envMap(env)
	askpass := m["GIT_ASKPASS"]
	if askpass == "" {
		t.Fatal("GIT_ASKPASS not set")
	}
	if _, err := os.Stat(askpass); err != nil {
		t.Errorf("askpass helper not written: %v", err)
	}
	if m["GIT_ASKPASS_USER"] != "u" || m["GIT_ASKPASS_PASS"] != "p" {
		t.Errorf("askpass user/pass = %q/%q, want u/p", m["GIT_ASKPASS_USER"], m["GIT_ASKPASS_PASS"])
	}

	// cleanup removes the temp material.
	cleanup()
	if _, err := os.Stat(askpass); !os.IsNotExist(err) {
		t.Errorf("askpass helper not cleaned up: %v", err)
	}
}

func TestEnv_SSHRequiresKey(t *testing.T) {
	if _, _, err := (&Creds{Username: "u"}).Env("git@github.com:o/r.git"); err == nil {
		t.Error("ssh repo without a key should error")
	}
}

func TestEnv_SSHKnownHosts(t *testing.T) {
	creds := &Creds{SSHKey: []byte("KEY"), KnownHosts: []byte("gh ssh-ed25519 AAA")}
	env, cleanup, err := creds.Env("ssh://git@github.com/o/r")
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	defer cleanup()

	sshCmd := envMap(env)["GIT_SSH_COMMAND"]
	if !strings.Contains(sshCmd, "ssh -i ") {
		t.Errorf("GIT_SSH_COMMAND = %q, want ssh -i", sshCmd)
	}
	if !strings.Contains(sshCmd, "StrictHostKeyChecking=yes") {
		t.Errorf("GIT_SSH_COMMAND = %q, want strict host checking", sshCmd)
	}
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
