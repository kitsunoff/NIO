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

package apply

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadGitCreds reads git credentials from a mounted Secret directory and
// maps the standard keys; an empty path yields no credentials.
func TestLoadGitCreds(t *testing.T) {
	if creds, err := loadGitCreds(""); err != nil || creds != nil {
		t.Fatalf("loadGitCreds(\"\") = (%v, %v), want (nil, nil)", creds, err)
	}

	dir := t.TempDir()
	writeKey := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeKey("username", "git\n")
	writeKey("token", "ghp_secret\n")

	creds, err := loadGitCreds(dir)
	if err != nil {
		t.Fatalf("loadGitCreds: %v", err)
	}
	if creds == nil {
		t.Fatal("loadGitCreds returned nil, want credentials")
	}
	if creds.Username != "git" || creds.Password != "ghp_secret" {
		t.Errorf("creds = %+v, want username=git password=ghp_secret (token mapped, trimmed)", creds)
	}
}

// TestLoadGitCreds_WhitespacePasswordFallsBackToToken guards that a
// whitespace-only password triggers the token fallback, matching
// controller.readGitCredentials so both readers agree.
func TestLoadGitCreds_WhitespacePasswordFallsBackToToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "password"), []byte("  \n"), 0o600); err != nil {
		t.Fatalf("write password: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("ghp_fallback"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	creds, err := loadGitCreds(dir)
	if err != nil {
		t.Fatalf("loadGitCreds: %v", err)
	}
	if creds == nil || creds.Password != "ghp_fallback" {
		t.Errorf("creds = %+v, want password falling back to token ghp_fallback", creds)
	}
}

// setRequiredApplyEnv sets the minimal env the controller always provides.
func setRequiredApplyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NIO_GIT_REPO", "https://github.com/example/nixos.git")
	t.Setenv("NIO_GIT_REF", "main")
	t.Setenv("NIO_TARGET_HOST", "10.0.0.5")
}

// TestLoadConfigFromEnv_SSHKeyPathContract guards that apply consumes the SSH
// key as a mounted file path (NIO_SSH_KEY_PATH), matching how the controller
// mounts the key Secret as a volume.
func TestLoadConfigFromEnv_SSHKeyPathContract(t *testing.T) {
	setRequiredApplyEnv(t)
	t.Setenv("NIO_SSH_KEY_PATH", "/secrets/ssh/ssh-privatekey")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if cfg.SSHKeyPath != "/secrets/ssh/ssh-privatekey" {
		t.Errorf("SSHKeyPath = %q, want /secrets/ssh/ssh-privatekey", cfg.SSHKeyPath)
	}
}

// TestLoadConfigFromEnv_RequiresSSHKeyPath guards that a missing key path is a
// hard configuration error rather than a silent no-key apply.
func TestLoadConfigFromEnv_RequiresSSHKeyPath(t *testing.T) {
	setRequiredApplyEnv(t)
	// NIO_SSH_KEY_PATH intentionally unset.

	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error when NIO_SSH_KEY_PATH is missing, got nil")
	}
}
