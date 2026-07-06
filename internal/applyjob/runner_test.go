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

package applyjob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// MockCommandExecutor implements CommandExecutor for testing.
type MockCommandExecutor struct {
	RunFunc func(ctx context.Context, name string, args ...string) (string, error)
	Calls   []CommandCall
}

// CommandCall records a command execution.
type CommandCall struct {
	Name string
	Args []string
}

func (m *MockCommandExecutor) Run(ctx context.Context, name string, args ...string) (string, error) {
	m.Calls = append(m.Calls, CommandCall{Name: name, Args: args})
	if m.RunFunc != nil {
		return m.RunFunc(ctx, name, args...)
	}
	return "", nil
}

func TestGitClone_Success(t *testing.T) {
	tempDir := t.TempDir()
	executor := &MockCommandExecutor{
		RunFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			// Simulate successful clone by creating the directory
			if name == "git" && len(args) > 0 && args[0] == "clone" {
				repoDir := args[len(args)-1]
				if err := os.MkdirAll(repoDir, 0755); err != nil {
					return "", err
				}
			}
			return "Cloning into 'repo'...", nil
		},
	}

	runner := &Runner{
		Executor: executor,
		WorkDir:  tempDir,
	}

	config := &JobConfig{
		GitRepo: "https://github.com/example/nixos-config.git",
		GitRef:  "main",
	}

	repoPath, err := runner.CloneRepository(context.Background(), config)
	if err != nil {
		t.Fatalf("CloneRepository failed: %v", err)
	}

	if repoPath == "" {
		t.Error("expected non-empty repo path")
	}

	// Verify git clone was called
	if len(executor.Calls) < 1 {
		t.Fatal("expected at least one command call")
	}

	cloneCall := executor.Calls[0]
	if cloneCall.Name != "git" {
		t.Errorf("expected git command, got %s", cloneCall.Name)
	}
	if cloneCall.Args[0] != "clone" {
		t.Errorf("expected clone subcommand, got %s", cloneCall.Args[0])
	}
}

func TestGitClone_RepoNotFound(t *testing.T) {
	tempDir := t.TempDir()
	executor := &MockCommandExecutor{
		RunFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			return "", errors.New("fatal: repository 'https://github.com/example/nonexistent.git' not found")
		},
	}

	runner := &Runner{
		Executor: executor,
		WorkDir:  tempDir,
	}

	config := &JobConfig{
		GitRepo: "https://github.com/example/nonexistent.git",
		GitRef:  "main",
	}

	_, err := runner.CloneRepository(context.Background(), config)
	if err == nil {
		t.Fatal("expected error for non-existent repo")
	}

	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Errorf("expected GitError, got %T", err)
	}
}

func TestGitClone_AuthFailed(t *testing.T) {
	tempDir := t.TempDir()
	executor := &MockCommandExecutor{
		RunFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			return "", errors.New("fatal: could not read Username for 'https://github.com': terminal prompts disabled")
		},
	}

	runner := &Runner{
		Executor: executor,
		WorkDir:  tempDir,
	}

	config := &JobConfig{
		GitRepo: "https://github.com/private/repo.git",
		GitRef:  "main",
	}

	_, err := runner.CloneRepository(context.Background(), config)
	if err == nil {
		t.Fatal("expected error for auth failure")
	}

	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Errorf("expected GitError, got %T", err)
	}
}

func TestNixosRebuild_Success(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	executor := &MockCommandExecutor{
		RunFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			return "building the system configuration...\nactivating the configuration...\n", nil
		},
	}

	runner := &Runner{
		Executor: executor,
		WorkDir:  tempDir,
	}

	config := &JobConfig{
		TargetHost: "192.168.1.100",
		SSHUser:    "root",
		SSHKeyPath: "/tmp/ssh-key",
		Flake:      "#worker",
		Operation:  OperationNixosRebuild,
	}

	err := runner.ApplyConfiguration(context.Background(), repoPath, config)
	if err != nil {
		t.Fatalf("ApplyConfiguration failed: %v", err)
	}

	// Verify nixos-rebuild was called
	found := false
	for _, call := range executor.Calls {
		if call.Name == "nix" {
			found = true
			// Check for expected args
			hasNixosRebuild := false
			hasSwitch := false
			for _, arg := range call.Args {
				if arg == "nixpkgs#nixos-rebuild" {
					hasNixosRebuild = true
				}
				if arg == "switch" {
					hasSwitch = true
				}
			}
			if !hasNixosRebuild || !hasSwitch {
				t.Error("expected nixos-rebuild switch command")
			}
			break
		}
	}
	if !found {
		t.Error("expected nix command to be called")
	}
}

func TestNixosRebuild_BuildError(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	executor := &MockCommandExecutor{
		RunFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			return "error: builder for '/nix/store/xxx.drv' failed with exit code 1", errors.New("exit status 1")
		},
	}

	runner := &Runner{
		Executor: executor,
		WorkDir:  tempDir,
	}

	config := &JobConfig{
		TargetHost: "192.168.1.100",
		SSHUser:    "root",
		SSHKeyPath: "/tmp/ssh-key",
		Flake:      "#worker",
		Operation:  OperationNixosRebuild,
	}

	err := runner.ApplyConfiguration(context.Background(), repoPath, config)
	if err == nil {
		t.Fatal("expected error for build failure")
	}

	var applyErr *ApplyError
	if !errors.As(err, &applyErr) {
		t.Errorf("expected ApplyError, got %T", err)
	}
}

func TestNixosAnywhere_Success(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	executor := &MockCommandExecutor{
		RunFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			return "Installing NixOS...\nInstallation complete!\n", nil
		},
	}

	runner := &Runner{
		Executor: executor,
		WorkDir:  tempDir,
	}

	config := &JobConfig{
		TargetHost: "192.168.1.100",
		SSHUser:    "root",
		SSHKeyPath: "/tmp/ssh-key",
		Flake:      "#worker",
		Operation:  OperationFullInstall,
	}

	err := runner.ApplyConfiguration(context.Background(), repoPath, config)
	if err != nil {
		t.Fatalf("ApplyConfiguration failed: %v", err)
	}

	// Verify nixos-anywhere was called
	found := false
	for _, call := range executor.Calls {
		if call.Name == "nix" {
			for _, arg := range call.Args {
				if arg == "nixos-anywhere" || arg == "github:nix-community/nixos-anywhere" {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Error("expected nixos-anywhere to be called")
	}
}

func TestNixosAnywhere_Failure(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	executor := &MockCommandExecutor{
		RunFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			return "Error: SSH connection refused", errors.New("exit status 1")
		},
	}

	runner := &Runner{
		Executor: executor,
		WorkDir:  tempDir,
	}

	config := &JobConfig{
		TargetHost: "192.168.1.100",
		SSHUser:    "root",
		SSHKeyPath: "/tmp/ssh-key",
		Flake:      "#worker",
		Operation:  OperationFullInstall,
	}

	err := runner.ApplyConfiguration(context.Background(), repoPath, config)
	if err == nil {
		t.Fatal("expected error for failed install")
	}
}

func TestTimeout_Handling(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	executor := &MockCommandExecutor{
		RunFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			// Simulate a slow operation
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(5 * time.Second):
				return "done", nil
			}
		},
	}

	runner := &Runner{
		Executor: executor,
		WorkDir:  tempDir,
	}

	config := &JobConfig{
		TargetHost: "192.168.1.100",
		SSHUser:    "root",
		SSHKeyPath: "/tmp/ssh-key",
		Flake:      "#worker",
		Operation:  OperationNixosRebuild,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := runner.ApplyConfiguration(ctx, repoPath, config)
	if err == nil {
		t.Fatal("expected timeout error")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestAdditionalFiles_Inline(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{
		Executor: &MockCommandExecutor{},
		WorkDir:  tempDir,
	}

	files := []AdditionalFile{
		{
			Path:    "secrets/wifi.nix",
			Content: "{ ssid = \"test\"; password = \"secret\"; }",
		},
		{
			Path:    "hosts/worker.nix",
			Content: "{ hostname = \"worker-01\"; }",
		},
	}

	err := runner.InjectAdditionalFiles(repoPath, files)
	if err != nil {
		t.Fatalf("InjectAdditionalFiles failed: %v", err)
	}

	// Verify files were created
	for _, f := range files {
		fullPath := filepath.Join(repoPath, f.Path)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Errorf("failed to read %s: %v", f.Path, err)
			continue
		}
		if string(content) != f.Content {
			t.Errorf("content mismatch for %s: got %q, want %q", f.Path, string(content), f.Content)
		}
	}
}

func TestAdditionalFiles_CreatesDirs(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{
		Executor: &MockCommandExecutor{},
		WorkDir:  tempDir,
	}

	files := []AdditionalFile{
		{
			Path:    "deep/nested/dir/file.nix",
			Content: "{}",
		},
	}

	err := runner.InjectAdditionalFiles(repoPath, files)
	if err != nil {
		t.Fatalf("InjectAdditionalFiles failed: %v", err)
	}

	// Verify directory structure was created
	fullPath := filepath.Join(repoPath, "deep/nested/dir/file.nix")
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		t.Error("expected nested file to be created")
	}
}

func TestSSHKeySetup(t *testing.T) {
	tempDir := t.TempDir()

	runner := &Runner{
		Executor: &MockCommandExecutor{},
		WorkDir:  tempDir,
	}

	privateKey := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ntest-key-content\n-----END OPENSSH PRIVATE KEY-----")

	keyPath, cleanup, err := runner.SetupSSHKey(privateKey)
	if err != nil {
		t.Fatalf("SetupSSHKey failed: %v", err)
	}
	defer cleanup()

	// Verify key file exists with correct permissions
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file not found: %v", err)
	}

	// Check permissions (should be 0600)
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected permissions 0600, got %v", info.Mode().Perm())
	}

	// Verify content
	content, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("failed to read key: %v", err)
	}
	if string(content) != string(privateKey) {
		t.Error("key content mismatch")
	}

	// Call cleanup and verify file is removed
	cleanup()
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Error("expected key file to be cleaned up")
	}
}
