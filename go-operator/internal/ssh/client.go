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

package ssh

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// Client defines the interface for SSH operations.
// This interface allows for easy mocking in tests.
type Client interface {
	// CheckConnection tests SSH connectivity to the host.
	// Returns nil if connection successful, error otherwise.
	CheckConnection(ctx context.Context, host string, port int, config *Config) error

	// RunCommand executes a command on the remote host and returns output.
	RunCommand(ctx context.Context, host string, port int, config *Config, command string) (string, error)
}

// Config holds SSH connection configuration.
type Config struct {
	// User is the SSH username.
	User string

	// PrivateKey is the PEM-encoded private key for authentication.
	PrivateKey []byte

	// Password is the password for authentication (fallback).
	Password string

	// Timeout is the connection timeout.
	Timeout time.Duration
}

// DefaultClient is the production SSH client implementation.
type DefaultClient struct{}

// NewClient creates a new SSH client.
func NewClient() Client {
	return &DefaultClient{}
}

// CheckConnection tests SSH connectivity to the host.
func (c *DefaultClient) CheckConnection(ctx context.Context, host string, port int, config *Config) error {
	sshConfig, err := c.buildSSHConfig(config)
	if err != nil {
		return fmt.Errorf("build ssh config: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", host, port)

	// Create connection with timeout
	dialer := net.Dialer{Timeout: config.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("tcp dial: %w", err)
	}
	defer conn.Close()

	// Set deadline for SSH handshake
	deadline, ok := ctx.Deadline()
	if ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set deadline: %w", err)
		}
	}

	// Perform SSH handshake
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, sshConfig)
	if err != nil {
		return fmt.Errorf("ssh handshake: %w", err)
	}
	defer sshConn.Close()

	// Create client for proper cleanup
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	return nil
}

// RunCommand executes a command on the remote host.
func (c *DefaultClient) RunCommand(ctx context.Context, host string, port int, config *Config, command string) (string, error) {
	sshConfig, err := c.buildSSHConfig(config)
	if err != nil {
		return "", fmt.Errorf("build ssh config: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", host, port)

	// Create connection with timeout
	dialer := net.Dialer{Timeout: config.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("tcp dial: %w", err)
	}
	defer conn.Close()

	// Set deadline for SSH handshake
	deadline, ok := ctx.Deadline()
	if ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return "", fmt.Errorf("set deadline: %w", err)
		}
	}

	// Perform SSH handshake
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, sshConfig)
	if err != nil {
		return "", fmt.Errorf("ssh handshake: %w", err)
	}
	defer sshConn.Close()

	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return string(output), fmt.Errorf("run command: %w", err)
	}

	return string(output), nil
}

// buildSSHConfig creates ssh.ClientConfig from Config.
func (c *DefaultClient) buildSSHConfig(config *Config) (*ssh.ClientConfig, error) {
	var authMethods []ssh.AuthMethod

	// Try private key authentication first
	if len(config.PrivateKey) > 0 {
		signer, err := ssh.ParsePrivateKey(config.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	// Add password authentication as fallback
	if config.Password != "" {
		authMethods = append(authMethods, ssh.Password(config.Password))
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no authentication method configured")
	}

	return &ssh.ClientConfig{
		User:            config.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Implement proper host key verification
		Timeout:         config.Timeout,
	}, nil
}

// MockClient is a mock SSH client for testing.
type MockClient struct {
	// CheckConnectionFunc is called when CheckConnection is invoked.
	CheckConnectionFunc func(ctx context.Context, host string, port int, config *Config) error

	// RunCommandFunc is called when RunCommand is invoked.
	RunCommandFunc func(ctx context.Context, host string, port int, config *Config, command string) (string, error)
}

// CheckConnection calls the mock function.
func (m *MockClient) CheckConnection(ctx context.Context, host string, port int, config *Config) error {
	if m.CheckConnectionFunc != nil {
		return m.CheckConnectionFunc(ctx, host, port, config)
	}
	return nil
}

// RunCommand calls the mock function.
func (m *MockClient) RunCommand(ctx context.Context, host string, port int, config *Config, command string) (string, error) {
	if m.RunCommandFunc != nil {
		return m.RunCommandFunc(ctx, host, port, config, command)
	}
	return "", nil
}
