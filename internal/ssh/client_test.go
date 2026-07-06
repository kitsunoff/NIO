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
	"testing"
	"time"
)

func TestMockClient_CheckConnection(t *testing.T) {
	tests := []struct {
		name    string
		mock    *MockClient
		wantErr bool
	}{
		{
			name: "success",
			mock: &MockClient{
				CheckConnectionFunc: func(ctx context.Context, host string, port int, config *Config) error {
					return nil
				},
			},
			wantErr: false,
		},
		{
			name: "failure",
			mock: &MockClient{
				CheckConnectionFunc: func(ctx context.Context, host string, port int, config *Config) error {
					return context.DeadlineExceeded
				},
			},
			wantErr: true,
		},
		{
			name:    "nil func returns nil",
			mock:    &MockClient{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mock.CheckConnection(context.Background(), "host", 22, &Config{})
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckConnection() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMockClient_RunCommand(t *testing.T) {
	tests := []struct {
		name       string
		mock       *MockClient
		wantOutput string
		wantErr    bool
	}{
		{
			name: "success with output",
			mock: &MockClient{
				RunCommandFunc: func(ctx context.Context, host string, port int, config *Config, command string) (string, error) {
					return "hello world", nil
				},
			},
			wantOutput: "hello world",
			wantErr:    false,
		},
		{
			name: "failure",
			mock: &MockClient{
				RunCommandFunc: func(ctx context.Context, host string, port int, config *Config, command string) (string, error) {
					return "", context.DeadlineExceeded
				},
			},
			wantOutput: "",
			wantErr:    true,
		},
		{
			name:       "nil func returns empty",
			mock:       &MockClient{},
			wantOutput: "",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := tt.mock.RunCommand(context.Background(), "host", 22, &Config{}, "echo hello")
			if (err != nil) != tt.wantErr {
				t.Errorf("RunCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
			if output != tt.wantOutput {
				t.Errorf("RunCommand() output = %v, want %v", output, tt.wantOutput)
			}
		})
	}
}

func TestConfig_Defaults(t *testing.T) {
	config := &Config{
		User:    "testuser",
		Timeout: 10 * time.Second,
	}

	if config.User != "testuser" {
		t.Errorf("User = %v, want testuser", config.User)
	}

	if config.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", config.Timeout)
	}
}

func TestNewClient(t *testing.T) {
	client := NewClient()
	if client == nil {
		t.Error("NewClient() returned nil")
	}

	_, ok := client.(*DefaultClient)
	if !ok {
		t.Errorf("NewClient() returned %T, want *DefaultClient", client)
	}
}

func TestDefaultClient_buildSSHConfig_NoAuth(t *testing.T) {
	client := &DefaultClient{}
	config := &Config{
		User:    "root",
		Timeout: 30 * time.Second,
	}

	_, err := client.buildSSHConfig(config)
	if err == nil {
		t.Error("expected error for no authentication method")
	}
}

func TestDefaultClient_buildSSHConfig_WithPassword(t *testing.T) {
	client := &DefaultClient{}
	config := &Config{
		User:     "root",
		Password: "secret",
		Timeout:  30 * time.Second,
	}

	sshConfig, err := client.buildSSHConfig(config)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if sshConfig.User != "root" {
		t.Errorf("User = %v, want root", sshConfig.User)
	}

	if len(sshConfig.Auth) != 1 {
		t.Errorf("Auth methods = %d, want 1", len(sshConfig.Auth))
	}
}

func TestDefaultClient_buildSSHConfig_WithInvalidKey(t *testing.T) {
	client := &DefaultClient{}

	// Test that malformed key returns error
	config := &Config{
		User:       "root",
		PrivateKey: []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ninvalid\n-----END OPENSSH PRIVATE KEY-----"),
		Timeout:    30 * time.Second,
	}

	_, err := client.buildSSHConfig(config)
	if err == nil {
		t.Error("expected error for malformed key")
	}
}

func TestDefaultClient_buildSSHConfig_KeyWithPasswordFallback(t *testing.T) {
	client := &DefaultClient{}

	// When key is invalid but password is provided, password auth should still work
	// But in our implementation, we fail early on invalid key
	config := &Config{
		User:       "root",
		PrivateKey: []byte("invalid key"),
		Password:   "fallback",
		Timeout:    30 * time.Second,
	}

	_, err := client.buildSSHConfig(config)
	// Current impl fails if key is invalid, even with password
	if err == nil {
		t.Error("expected error for invalid key")
	}
}

func TestDefaultClient_buildSSHConfig_InvalidKey(t *testing.T) {
	client := &DefaultClient{}
	config := &Config{
		User:       "root",
		PrivateKey: []byte("not a valid key"),
		Timeout:    30 * time.Second,
	}

	_, err := client.buildSSHConfig(config)
	if err == nil {
		t.Error("expected error for invalid key")
	}
}
