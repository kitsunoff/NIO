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

// Package apply implements the apply subcommand for running NixOS apply operations.
package apply

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kitsunoff/nixos-operator/internal/applyjob"
)

// Config holds the apply command configuration.
type Config struct {
	// ConfigName is the NixosConfiguration resource name.
	ConfigName string
	// ConfigNamespace is the NixosConfiguration resource namespace.
	ConfigNamespace string
	// Operation is the operation type (NixosRebuild or FullInstall).
	Operation string
	// GitRepo is the git repository URL.
	GitRepo string
	// GitRef is the git ref to checkout.
	GitRef string
	// ConfigSubdir is the subdirectory containing the nix configuration.
	ConfigSubdir string
	// Flake is the flake reference (e.g., "#worker").
	Flake string
	// TargetHost is the target machine hostname or IP.
	TargetHost string
	// SSHUser is the SSH username.
	SSHUser string
	// SSHKeyBase64 is the base64-encoded SSH private key.
	SSHKeyBase64 string
	// AdditionalFilesJSON is JSON-encoded additional files.
	AdditionalFilesJSON string
	// Timeout is the operation timeout.
	Timeout time.Duration
	// WorkDir is the working directory.
	WorkDir string
}

// LoadConfigFromEnv loads configuration from environment variables.
func LoadConfigFromEnv() (*Config, error) {
	config := &Config{
		ConfigName:          os.Getenv("NIO_CONFIG_NAME"),
		ConfigNamespace:     os.Getenv("NIO_CONFIG_NAMESPACE"),
		Operation:           os.Getenv("NIO_OPERATION"),
		GitRepo:             os.Getenv("NIO_GIT_REPO"),
		GitRef:              os.Getenv("NIO_GIT_REF"),
		ConfigSubdir:        os.Getenv("NIO_CONFIG_SUBDIR"),
		Flake:               os.Getenv("NIO_FLAKE"),
		TargetHost:          os.Getenv("NIO_TARGET_HOST"),
		SSHUser:             os.Getenv("NIO_SSH_USER"),
		SSHKeyBase64:        os.Getenv("NIO_SSH_KEY"),
		AdditionalFilesJSON: os.Getenv("NIO_ADDITIONAL_FILES"),
		WorkDir:             os.Getenv("NIO_WORK_DIR"),
	}

	// Parse timeout
	timeoutStr := os.Getenv("NIO_TIMEOUT")
	if timeoutStr != "" {
		timeout, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout: %w", err)
		}
		config.Timeout = timeout
	} else {
		config.Timeout = 30 * time.Minute
	}

	// Set defaults
	if config.WorkDir == "" {
		config.WorkDir = "/tmp/nio-apply"
	}
	if config.SSHUser == "" {
		config.SSHUser = "root"
	}
	if config.Operation == "" {
		config.Operation = "NixosRebuild"
	}

	// Validate required fields
	if config.GitRepo == "" {
		return nil, fmt.Errorf("NIO_GIT_REPO is required")
	}
	if config.GitRef == "" {
		return nil, fmt.Errorf("NIO_GIT_REF is required")
	}
	if config.TargetHost == "" {
		return nil, fmt.Errorf("NIO_TARGET_HOST is required")
	}
	if config.SSHKeyBase64 == "" {
		return nil, fmt.Errorf("NIO_SSH_KEY is required")
	}

	return config, nil
}

// Run executes the apply command.
func Run() error {
	fmt.Println("nixos-operator apply starting...")

	config, err := LoadConfigFromEnv()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Printf("Config: name=%s namespace=%s operation=%s\n",
		config.ConfigName, config.ConfigNamespace, config.Operation)
	fmt.Printf("Git: repo=%s ref=%s subdir=%s flake=%s\n",
		config.GitRepo, config.GitRef, config.ConfigSubdir, config.Flake)
	fmt.Printf("Target: host=%s user=%s timeout=%s\n",
		config.TargetHost, config.SSHUser, config.Timeout)

	// Decode SSH key
	sshKey, err := base64.StdEncoding.DecodeString(config.SSHKeyBase64)
	if err != nil {
		return fmt.Errorf("decode ssh key: %w", err)
	}

	// Parse additional files
	var additionalFiles []applyjob.AdditionalFile
	if config.AdditionalFilesJSON != "" {
		if err := json.Unmarshal([]byte(config.AdditionalFilesJSON), &additionalFiles); err != nil {
			return fmt.Errorf("parse additional files: %w", err)
		}
	}

	// Create working directory
	if err := os.MkdirAll(config.WorkDir, 0755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}

	// Setup context with timeout and signal handling
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		fmt.Printf("Received signal %v, cancelling...\n", sig)
		cancel()
	}()

	// Create runner and execute
	runner := applyjob.NewRunner(config.WorkDir)

	jobConfig := &applyjob.JobConfig{
		GitRepo:      config.GitRepo,
		GitRef:       config.GitRef,
		ConfigSubdir: config.ConfigSubdir,
		Flake:        config.Flake,
		TargetHost:   config.TargetHost,
		SSHUser:      config.SSHUser,
		Operation:    applyjob.OperationType(config.Operation),
	}

	fmt.Println("Starting apply operation...")
	if err := runner.Run(ctx, jobConfig, sshKey, additionalFiles); err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}

	fmt.Println("Apply completed successfully!")
	return nil
}
