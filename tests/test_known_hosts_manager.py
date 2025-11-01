#!/usr/bin/env python3

"""Unit tests for SSH known_hosts manager module."""

import pytest
import tempfile
import os
from pathlib import Path
from unittest.mock import patch, MagicMock
from known_hosts_manager import KnownHostsManager, get_known_hosts_manager


class TestKnownHostsManagerInitialization:
    """Tests for KnownHostsManager initialization."""

    def test_init_with_custom_storage_path(self):
        """Should initialize with custom storage path."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "custom_known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            assert manager.known_hosts_path == Path(storage_path)
            assert manager.known_hosts_path.exists()
            # Check file permissions (0o600 = rw-------)
            assert oct(manager.known_hosts_path.stat().st_mode)[-3:] == "600"

    def test_init_with_default_storage_path(self):
        """Should initialize with default storage path from config."""
        with patch("known_hosts_manager.config.KNOWN_HOSTS_PATH", "/tmp/test-known-hosts"):
            manager = KnownHostsManager()

            assert manager.known_hosts_path == Path("/tmp/test-known-hosts/known_hosts")
            assert manager.known_hosts_path.exists()

    def test_get_known_hosts_path(self):
        """Should return known_hosts file path as string."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            path = manager.get_known_hosts_path()

            assert isinstance(path, str)
            assert path == str(manager.known_hosts_path)


class TestKnownHostsManagerAddHostKey:
    """Tests for adding host keys."""

    def test_add_new_host_key(self):
        """Should add new host key to known_hosts."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            hostname = "192.168.1.100"
            key_type = "ssh-ed25519"
            public_key = "AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTesting123456"

            manager.add_host_key(hostname, key_type, public_key)

            with open(manager.known_hosts_path, "r") as f:
                content = f.read()
                assert f"{hostname} {key_type} {public_key}" in content

    def test_add_duplicate_host_key(self):
        """Should not add duplicate host key."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            hostname = "192.168.1.100"
            key_type = "ssh-ed25519"
            public_key = "AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTesting123456"

            # Add key twice
            manager.add_host_key(hostname, key_type, public_key)
            manager.add_host_key(hostname, key_type, public_key)

            with open(manager.known_hosts_path, "r") as f:
                content = f.read()
                # Should appear only once
                assert content.count(f"{hostname} {key_type} {public_key}") == 1

    def test_add_multiple_different_hosts(self):
        """Should add multiple different host keys."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            hosts = [
                ("host1.example.com", "ssh-ed25519", "AAAAC3NzaC1lZDI1NTE5AAAAIKey1"),
                ("host2.example.com", "ecdsa-sha2-nistp256", "AAAAE2VjZHNhKey2"),
                ("192.168.1.100", "ssh-rsa", "AAAAB3NzaC1yc2EAAAAKey3"),
            ]

            for hostname, key_type, public_key in hosts:
                manager.add_host_key(hostname, key_type, public_key)

            with open(manager.known_hosts_path, "r") as f:
                content = f.read()
                for hostname, key_type, public_key in hosts:
                    assert f"{hostname} {key_type} {public_key}" in content


class TestKnownHostsManagerTOFU:
    """Tests for Trust On First Use functionality."""

    def test_tofu_first_connection(self):
        """Should return True for first connection (new host)."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            result = manager.trust_on_first_use("new-host.example.com")

            assert result is True

    def test_tofu_subsequent_connection_standard_port(self):
        """Should return False for known host on standard port."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            hostname = "known-host.example.com"
            manager.add_host_key(hostname, "ssh-ed25519", "FakePublicKey123")

            result = manager.trust_on_first_use(hostname)

            assert result is False

    def test_tofu_subsequent_connection_custom_port(self):
        """Should return False for known host on custom port."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            hostname = "192.168.1.100"
            port = 2222
            # Add host with port notation [hostname]:port
            with open(manager.known_hosts_path, "a") as f:
                f.write(f"[{hostname}]:{port} ssh-ed25519 FakeKey\n")

            result = manager.trust_on_first_use(hostname, port)

            assert result is False

    def test_tofu_nonexistent_known_hosts_file(self):
        """Should return True when known_hosts file doesn't exist."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            # Delete the file that was created during init
            manager.known_hosts_path.unlink()

            result = manager.trust_on_first_use("any-host.example.com")

            assert result is True


class TestKnownHostsManagerClearHost:
    """Tests for clearing host keys."""

    def test_clear_existing_host(self):
        """Should remove all entries for specified host."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            # Add multiple hosts
            manager.add_host_key("host1.example.com", "ssh-ed25519", "Key1")
            manager.add_host_key("host2.example.com", "ssh-ed25519", "Key2")
            manager.add_host_key("host3.example.com", "ssh-ed25519", "Key3")

            manager.clear_host("host2.example.com")

            with open(manager.known_hosts_path, "r") as f:
                content = f.read()
                assert "host1.example.com" in content
                assert "host2.example.com" not in content
                assert "host3.example.com" in content

    def test_clear_host_with_port_notation(self):
        """Should remove host with port notation [hostname]:port."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            hostname = "192.168.1.100"
            # Add host with port notation
            with open(manager.known_hosts_path, "a") as f:
                f.write(f"[{hostname}]:2222 ssh-ed25519 FakeKey\n")

            manager.clear_host(hostname)

            with open(manager.known_hosts_path, "r") as f:
                content = f.read()
                assert hostname not in content

    def test_clear_nonexistent_host(self):
        """Should handle clearing nonexistent host gracefully."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            manager.add_host_key("host1.example.com", "ssh-ed25519", "Key1")

            # Clear nonexistent host - should not raise error
            manager.clear_host("nonexistent.example.com")

            with open(manager.known_hosts_path, "r") as f:
                content = f.read()
                assert "host1.example.com" in content

    def test_clear_host_when_file_does_not_exist(self):
        """Should handle clearing when known_hosts file doesn't exist."""
        with tempfile.TemporaryDirectory() as temp_dir:
            storage_path = os.path.join(temp_dir, "known_hosts")
            manager = KnownHostsManager(storage_path=storage_path)

            # Delete the file
            manager.known_hosts_path.unlink()

            # Should not raise error
            manager.clear_host("any-host.example.com")


class TestGlobalKnownHostsManager:
    """Tests for global singleton manager."""

    def test_get_known_hosts_manager_singleton(self):
        """Should return same instance on multiple calls."""
        # Reset global state
        import known_hosts_manager
        known_hosts_manager._known_hosts_manager = None

        manager1 = get_known_hosts_manager()
        manager2 = get_known_hosts_manager()

        assert manager1 is manager2

    def test_get_known_hosts_manager_creates_instance(self):
        """Should create instance if none exists."""
        import known_hosts_manager
        known_hosts_manager._known_hosts_manager = None

        manager = get_known_hosts_manager()

        assert isinstance(manager, KnownHostsManager)
        assert manager is not None
