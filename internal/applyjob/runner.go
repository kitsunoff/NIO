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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// OperationType defines the type of apply operation.
type OperationType string

const (
	// OperationNixosRebuild uses nixos-rebuild switch.
	OperationNixosRebuild OperationType = "NixosRebuild"
	// OperationFullInstall uses nixos-anywhere for fresh installs.
	OperationFullInstall OperationType = "FullInstall"
)

// JobConfig holds configuration for an apply job.
type JobConfig struct {
	// Git repository URL
	GitRepo string
	// Git ref (branch, tag, or commit)
	GitRef string
	// Configuration subdirectory within repo
	ConfigSubdir string
	// Flake reference (e.g., "#worker")
	Flake string
	// Target host for SSH connection
	TargetHost string
	// SSH username
	SSHUser string
	// Path to SSH private key file
	SSHKeyPath string
	// Operation type (NixosRebuild or FullInstall)
	Operation OperationType
}

// AdditionalFile represents a file to inject into the repository.
type AdditionalFile struct {
	// Path relative to repository root
	Path string
	// File content
	Content string
}

// GitError represents a git operation error.
type GitError struct {
	Operation string
	Output    string
	Err       error
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s failed: %s: %v", e.Operation, e.Output, e.Err)
}

func (e *GitError) Unwrap() error {
	return e.Err
}

// ApplyError represents an apply operation error.
type ApplyError struct {
	Operation OperationType
	Output    string
	Err       error
}

func (e *ApplyError) Error() string {
	return fmt.Sprintf("%s failed: %s: %v", e.Operation, e.Output, e.Err)
}

func (e *ApplyError) Unwrap() error {
	return e.Err
}

// CommandExecutor executes shell commands.
type CommandExecutor interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// DefaultExecutor is the production command executor.
type DefaultExecutor struct{}

// Run executes a command and returns combined output.
func (e *DefaultExecutor) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// Runner executes apply jobs.
type Runner struct {
	Executor CommandExecutor
	WorkDir  string
}

// NewRunner creates a new Runner with default executor.
func NewRunner(workDir string) *Runner {
	return &Runner{
		Executor: &DefaultExecutor{},
		WorkDir:  workDir,
	}
}

// CloneRepository clones the git repository and checks out the specified ref.
func (r *Runner) CloneRepository(ctx context.Context, config *JobConfig) (string, error) {
	repoPath := filepath.Join(r.WorkDir, "repo")

	// Clone the repository
	args := []string{"clone", "--depth", "1", "--branch", config.GitRef, config.GitRepo, repoPath}
	output, err := r.Executor.Run(ctx, "git", args...)
	if err != nil {
		return "", &GitError{
			Operation: "clone",
			Output:    output,
			Err:       err,
		}
	}

	return repoPath, nil
}

// InjectAdditionalFiles writes additional files into the repository.
func (r *Runner) InjectAdditionalFiles(repoPath string, files []AdditionalFile) error {
	for _, f := range files {
		fullPath := filepath.Join(repoPath, f.Path)

		// Create parent directories
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}

		// Write file content
		if err := os.WriteFile(fullPath, []byte(f.Content), 0644); err != nil {
			return fmt.Errorf("write file %s: %w", f.Path, err)
		}
	}
	return nil
}

// ApplyConfiguration applies the NixOS configuration to the target host.
func (r *Runner) ApplyConfiguration(ctx context.Context, repoPath string, config *JobConfig) error {
	configPath := repoPath
	if config.ConfigSubdir != "" {
		configPath = filepath.Join(repoPath, config.ConfigSubdir)
	}

	// Set SSH options via environment
	sshOpts := fmt.Sprintf("-i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", config.SSHKeyPath)

	switch config.Operation {
	case OperationFullInstall:
		return r.runNixosAnywhere(ctx, configPath, config, sshOpts)
	case OperationNixosRebuild:
		return r.runNixosRebuild(ctx, configPath, config, sshOpts)
	default:
		return fmt.Errorf("unknown operation type: %s", config.Operation)
	}
}

// runNixosRebuild executes nixos-rebuild switch.
func (r *Runner) runNixosRebuild(ctx context.Context, configPath string, config *JobConfig, sshOpts string) error {
	targetHost := fmt.Sprintf("%s@%s", config.SSHUser, config.TargetHost)
	flakeRef := configPath + config.Flake

	// nix shell nixpkgs#nixos-rebuild --command nixos-rebuild switch --flake .#worker --target-host root@host
	args := []string{
		"--extra-experimental-features", "nix-command flakes",
		"shell", "nixpkgs#nixos-rebuild",
		"--command", "nixos-rebuild", "switch",
		"--flake", flakeRef,
		"--target-host", targetHost,
	}

	// Set NIX_SSHOPTS environment variable
	oldEnv := os.Getenv("NIX_SSHOPTS")
	_ = os.Setenv("NIX_SSHOPTS", sshOpts)
	defer func() { _ = os.Setenv("NIX_SSHOPTS", oldEnv) }()

	output, err := r.Executor.Run(ctx, "nix", args...)
	if err != nil {
		return &ApplyError{
			Operation: OperationNixosRebuild,
			Output:    output,
			Err:       err,
		}
	}

	return nil
}

// runNixosAnywhere executes nixos-anywhere for full disk installation.
func (r *Runner) runNixosAnywhere(ctx context.Context, configPath string, config *JobConfig, sshOpts string) error {
	targetHost := fmt.Sprintf("%s@%s", config.SSHUser, config.TargetHost)
	flakeRef := configPath + config.Flake

	// nix run github:nix-community/nixos-anywhere -- --flake .#worker root@host
	args := []string{
		"--extra-experimental-features", "nix-command flakes",
		"run", "github:nix-community/nixos-anywhere", "--",
		"--flake", flakeRef,
		targetHost,
	}

	// Add SSH options as separate arguments
	// nixos-anywhere expects: --ssh-option "StrictHostKeyChecking=no" --ssh-option "UserKnownHostsFile=/dev/null"
	// We also need to pass the identity file via environment since nixos-anywhere uses NIX_SSHOPTS internally
	oldEnv := os.Getenv("NIX_SSHOPTS")
	_ = os.Setenv("NIX_SSHOPTS", sshOpts)
	defer func() { _ = os.Setenv("NIX_SSHOPTS", oldEnv) }()

	output, err := r.Executor.Run(ctx, "nix", args...)
	if err != nil {
		return &ApplyError{
			Operation: OperationFullInstall,
			Output:    output,
			Err:       err,
		}
	}

	return nil
}

// SetupSSHKey writes the SSH private key to a temporary file with secure permissions.
// Returns the path to the key file and a cleanup function.
func (r *Runner) SetupSSHKey(privateKey []byte) (string, func(), error) {
	// Use /dev/shm for in-memory storage if available, fallback to temp dir
	keyDir := "/dev/shm"
	if _, err := os.Stat(keyDir); os.IsNotExist(err) {
		keyDir = r.WorkDir
	}

	keyPath := filepath.Join(keyDir, "ssh-key")

	// Write key with secure permissions (0600)
	if err := os.WriteFile(keyPath, privateKey, 0600); err != nil {
		return "", nil, fmt.Errorf("write ssh key: %w", err)
	}

	cleanup := func() {
		_ = os.Remove(keyPath)
	}

	return keyPath, cleanup, nil
}

// Run executes the full apply job workflow.
func (r *Runner) Run(ctx context.Context, config *JobConfig, sshKey []byte, additionalFiles []AdditionalFile) error {
	// Setup SSH key
	keyPath, cleanup, err := r.SetupSSHKey(sshKey)
	if err != nil {
		return fmt.Errorf("setup ssh key: %w", err)
	}
	defer cleanup()
	config.SSHKeyPath = keyPath

	// Clone repository
	repoPath, err := r.CloneRepository(ctx, config)
	if err != nil {
		return fmt.Errorf("clone repository: %w", err)
	}

	// Inject additional files
	if len(additionalFiles) > 0 {
		if err := r.InjectAdditionalFiles(repoPath, additionalFiles); err != nil {
			return fmt.Errorf("inject additional files: %w", err)
		}
	}

	// Apply configuration
	if err := r.ApplyConfiguration(ctx, repoPath, config); err != nil {
		return fmt.Errorf("apply configuration: %w", err)
	}

	return nil
}
