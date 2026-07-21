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

import "testing"

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
