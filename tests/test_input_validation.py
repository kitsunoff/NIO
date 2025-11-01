#!/usr/bin/env python3

"""Unit tests for input validation module."""

import pytest
from input_validation import (
    validate_hostname,
    validate_git_url,
    validate_ssh_username,
    validate_path,
    ValidationError,
)


class TestValidateHostname:
    """Tests for hostname validation."""

    def test_valid_hostnames(self):
        """Valid hostnames should pass validation."""
        valid = [
            "example.com",
            "subdomain.example.com",
            "192.168.1.1",
            "localhost",
            "host-name",
            "host.name.with.dots",
            "[::1]",  # IPv6
            "[2001:db8::1]",  # IPv6
        ]
        for hostname in valid:
            result = validate_hostname(hostname)
            assert result == hostname

    def test_invalid_hostnames(self):
        """Invalid hostnames should raise ValidationError."""
        invalid = [
            "host name",  # space
            "host;name",  # semicolon
            "host&name",  # ampersand
            "host|name",  # pipe
            "host`name",  # backtick
            "host$name",  # dollar
            "host(name)",  # parentheses
            "",  # empty
        ]
        for hostname in invalid:
            with pytest.raises(ValidationError):
                validate_hostname(hostname)


class TestValidateGitUrl:
    """Tests for Git URL validation."""

    def test_valid_git_urls(self):
        """Valid Git URLs should pass validation."""
        valid = [
            "https://github.com/owner/repo.git",
            "https://gitlab.com/owner/repo.git",
            "git@github.com:owner/repo.git",
            "ssh://git@github.com/owner/repo.git",
        ]
        for url in valid:
            result = validate_git_url(url)
            assert isinstance(result, str)

    def test_invalid_git_urls(self):
        """Invalid Git URLs should raise ValidationError."""
        invalid = [
            "ftp://malicious.com/repo.git",  # wrong protocol
            "https://github.com/owner/repo.git; rm -rf /",  # injection
            "file:///etc/passwd",  # file protocol
            "",  # empty
        ]
        for url in invalid:
            with pytest.raises(ValidationError):
                validate_git_url(url)


class TestValidateUsername:
    """Tests for username validation."""

    def test_valid_usernames(self):
        """Valid usernames should pass validation."""
        valid = [
            "root",
            "user123",
            "deploy-user",
            "user_name",
            "a",  # single char
        ]
        for username in valid:
            result = validate_ssh_username(username)
            assert result == username

    def test_invalid_usernames(self):
        """Invalid usernames should raise ValidationError."""
        invalid = [
            "user name",  # space
            "user;name",  # semicolon
            "user&name",  # ampersand
            "user|name",  # pipe
            "user`name",  # backtick
            "user$name",  # dollar
            "",  # empty
        ]
        for username in invalid:
            with pytest.raises(ValidationError):
                validate_ssh_username(username)


class TestValidatePath:
    """Tests for path validation."""

    def test_valid_paths(self):
        """Valid paths should pass validation."""
        valid = [
            "/etc/nixos/configuration.nix",
            "/home/user/file.txt",
            "/tmp/test",
            "relative/path/file.txt",
            "./file.txt",
        ]
        for path in valid:
            result = validate_path(path)
            assert result == path

    def test_invalid_paths(self):
        """Invalid paths should raise ValidationError."""
        invalid = [
            "/etc/nixos/config; rm -rf /",  # injection
            "/etc/passwd && cat /etc/shadow",  # injection
            "/tmp/`whoami`",  # command substitution
            "/tmp/$(whoami)",  # command substitution
            "",  # empty
        ]
        for path in invalid:
            with pytest.raises(ValidationError):
                validate_path(path)
