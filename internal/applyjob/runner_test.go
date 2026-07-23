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
	"strings"
	"testing"
	"time"

	"github.com/kitsunoff/nixos-operator/internal/gitauth"
)

const (
	// gitCmd is the git executable name asserted in command calls.
	gitCmd = "git"
	// gitCloneSubcmd is the git subcommand used for the shallow-clone fallback.
	gitCloneSubcmd = "clone"
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
			if name == gitCmd && len(args) > 0 && args[0] == gitCloneSubcmd {
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

	repoPath, err := runner.CloneRepository(context.Background(), config, nil)
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
	if cloneCall.Name != gitCmd {
		t.Errorf("expected git command, got %s", cloneCall.Name)
	}
	if cloneCall.Args[0] != gitCloneSubcmd {
		t.Errorf("expected clone subcommand, got %s", cloneCall.Args[0])
	}
}

// TestCloneRepository_FetchesResolvedSHA guards that when a resolved SHA is
// given, the Job fetches that exact commit and checks out FETCH_HEAD instead of
// plain-cloning the branch tip (which could have moved — the TOCTOU window).
func TestCloneRepository_FetchesResolvedSHA(t *testing.T) {
	tempDir := t.TempDir()
	executor := &MockCommandExecutor{}
	runner := &Runner{Executor: executor, WorkDir: tempDir}
	config := &JobConfig{
		GitRepo: "https://github.com/example/r.git",
		GitRef:  "main",
		GitRev:  "abc123sha",
	}

	if _, err := runner.CloneRepository(context.Background(), config, nil); err != nil {
		t.Fatalf("CloneRepository: %v", err)
	}

	var gotFetchSHA, gotCheckout, gotClone bool
	for _, c := range executor.Calls {
		joined := strings.Join(c.Args, " ")
		if strings.HasPrefix(joined, gitCloneSubcmd) {
			gotClone = true
		}
		if strings.Contains(joined, "fetch --depth 1 origin abc123sha") {
			gotFetchSHA = true
		}
		if strings.Contains(joined, "checkout -q FETCH_HEAD") {
			gotCheckout = true
		}
	}
	if gotClone {
		t.Error("used plain clone; want fetch-by-SHA to avoid the moving-tip TOCTOU")
	}
	if !gotFetchSHA {
		t.Error("did not fetch the resolved SHA")
	}
	if !gotCheckout {
		t.Error("did not checkout FETCH_HEAD")
	}
}

// TestRun_StagesInjectedFiles guards that after injecting additionalFiles the
// runner runs `git add --all` (so Nix includes them in the flake build) and
// that it happens before the nix apply.
func TestRun_StagesInjectedFiles(t *testing.T) {
	tempDir := t.TempDir()
	var calls []string
	executor := &MockCommandExecutor{
		RunFunc: func(_ context.Context, name string, args ...string) (string, error) {
			calls = append(calls, strings.Join(append([]string{name}, args...), " "))
			if name == gitCmd && len(args) > 0 && args[0] == gitCloneSubcmd {
				return "", os.MkdirAll(args[len(args)-1], 0o755)
			}
			return "", nil
		},
	}
	runner := &Runner{Executor: executor, WorkDir: tempDir}
	config := &JobConfig{
		GitRepo:    "https://github.com/example/r.git",
		GitRef:     "main",
		Operation:  OperationNixosRebuild,
		TargetHost: "host",
		SSHUser:    "root",
	}
	files := []AdditionalFile{{Path: "hardware.nix", Content: "{}"}}

	if err := runner.Run(context.Background(), config, []byte("KEY"), files, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	addIdx, nixIdx := -1, -1
	for i, c := range calls {
		if strings.HasPrefix(c, "git -C ") && strings.Contains(c, "add --force") && strings.Contains(c, "hardware.nix") {
			addIdx = i
		}
		if strings.HasPrefix(c, "nix ") {
			nixIdx = i
		}
	}
	if addIdx < 0 {
		t.Fatalf("git add --force <path> was not run after injecting files; calls=%v", calls)
	}
	if nixIdx < 0 || addIdx > nixIdx {
		t.Errorf("git add --all must run before the nix apply (add=%d nix=%d)", addIdx, nixIdx)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "repo", "hardware.nix")); err != nil {
		t.Errorf("injected file missing on disk: %v", err)
	}
}

func TestCloneRepository_WiresHTTPSCredentials(t *testing.T) {
	tempDir := t.TempDir()
	var sawAskpass, sawUser, sawPass string
	executor := &MockCommandExecutor{
		RunFunc: func(_ context.Context, _ string, args ...string) (string, error) {
			sawAskpass = os.Getenv("GIT_ASKPASS")
			sawUser = os.Getenv("GIT_ASKPASS_USER")
			sawPass = os.Getenv("GIT_ASKPASS_PASS")
			return "", os.MkdirAll(args[len(args)-1], 0o755)
		},
	}
	runner := &Runner{Executor: executor, WorkDir: tempDir}
	config := &JobConfig{GitRepo: "https://github.com/example/private.git", GitRef: "main"}
	const wantUser = "git"
	creds := &gitauth.Creds{Username: wantUser, Password: "ghp_tok"}

	if _, err := runner.CloneRepository(context.Background(), config, creds); err != nil {
		t.Fatalf("CloneRepository: %v", err)
	}
	if sawAskpass == "" {
		t.Error("GIT_ASKPASS was not set during the clone")
	}
	if sawUser != wantUser || sawPass != "ghp_tok" {
		t.Errorf("askpass creds during clone = %q/%q, want %s/ghp_tok", sawUser, sawPass, wantUser)
	}
	// The credential env must not leak past the clone.
	if _, ok := os.LookupEnv("GIT_ASKPASS"); ok {
		t.Error("GIT_ASKPASS leaked into the process env after the clone")
	}
}

func TestCloneRepository_WiresSSHCredentials(t *testing.T) {
	tempDir := t.TempDir()
	var sawSSHCmd string
	executor := &MockCommandExecutor{
		RunFunc: func(_ context.Context, _ string, args ...string) (string, error) {
			sawSSHCmd = os.Getenv("GIT_SSH_COMMAND")
			return "", os.MkdirAll(args[len(args)-1], 0o755)
		},
	}
	runner := &Runner{Executor: executor, WorkDir: tempDir}
	config := &JobConfig{GitRepo: "git@github.com:example/private.git", GitRef: "main"}
	creds := &gitauth.Creds{SSHKey: []byte("PRIVKEY"), KnownHosts: []byte("gh ssh-ed25519 AAA")}

	if _, err := runner.CloneRepository(context.Background(), config, creds); err != nil {
		t.Fatalf("CloneRepository: %v", err)
	}
	if !strings.Contains(sawSSHCmd, "ssh -i ") {
		t.Errorf("GIT_SSH_COMMAND = %q, want an ssh -i invocation", sawSSHCmd)
	}
	if !strings.Contains(sawSSHCmd, "StrictHostKeyChecking=yes") {
		t.Errorf("GIT_SSH_COMMAND = %q, want strict host checking with known_hosts", sawSSHCmd)
	}
	if _, ok := os.LookupEnv("GIT_SSH_COMMAND"); ok {
		t.Error("GIT_SSH_COMMAND leaked into the process env after the clone")
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

	_, err := runner.CloneRepository(context.Background(), config, nil)
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

	_, err := runner.CloneRepository(context.Background(), config, nil)
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
