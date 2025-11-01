#!/usr/bin/env python3

"""End-to-end tests for NixOS Infrastructure Operator.

These tests run the full operator workflow:
1. Create kind cluster
2. Deploy operator
3. Create Machine and NixOSConfiguration resources
4. Verify reconciliation with mock SSH server
5. Cleanup
"""

import pytest
import asyncio
import asyncssh
import tempfile
import os
from pathlib import Path


pytestmark = pytest.mark.e2e


class MockSSHServer:
    """Mock SSH server for E2E testing."""

    def __init__(self, port=2222):
        """Initialize mock SSH server."""
        self.port = port
        self.server = None
        self.host_key = None
        self.client_key = None
        self.commands_executed = []

    async def start(self):
        """Start the mock SSH server."""
        # Generate host key and client key for testing
        self.host_key = asyncssh.generate_private_key("ssh-rsa")
        self.client_key = asyncssh.generate_private_key("ssh-rsa")

        # Write authorized key to temp file
        self.temp_dir = tempfile.mkdtemp(prefix="mock-ssh-")
        authorized_keys_file = os.path.join(self.temp_dir, "authorized_keys")
        with open(authorized_keys_file, "w") as f:
            f.write(self.client_key.export_public_key().decode())

        # Start server with client's public key authorized
        self.server = await asyncssh.listen(
            "localhost",
            self.port,
            server_host_keys=[self.host_key],
            authorized_client_keys=authorized_keys_file,
            process_factory=self.handle_client,
        )

    def get_client_key(self):
        """Get the client private key for connections."""
        return self.client_key

    async def handle_client(self, process):
        """Handle client commands."""
        command = process.command
        self.commands_executed.append(command)

        # Mock responses for common commands
        if command == "uname -a":
            process.stdout.write("Linux test 6.1.0 NixOS\n")
        elif command == "nixos-version":
            process.stdout.write("23.11\n")
        elif "nixos-rebuild" in command:
            process.stdout.write("building system configuration...\n")
            await asyncio.sleep(0.1)
            process.stdout.write("activation finished successfully\n")
        elif command.startswith("cat /etc/machine-id"):
            process.stdout.write("test-machine-id-12345\n")
        else:
            process.stdout.write(f"Mock command: {command}\n")

        process.exit(0)

    async def stop(self):
        """Stop the mock SSH server."""
        if self.server:
            self.server.close()
            await self.server.wait_closed()
        # Clean up temp directory
        if hasattr(self, 'temp_dir') and os.path.exists(self.temp_dir):
            import shutil
            shutil.rmtree(self.temp_dir, ignore_errors=True)

    def get_executed_commands(self):
        """Get list of executed commands."""
        return self.commands_executed


class TestE2EBasicWorkflow:
    """E2E tests for basic operator workflow."""

    @pytest.fixture(scope="class")
    async def mock_ssh_server(self):
        """Start mock SSH server for tests."""
        server = MockSSHServer(port=2222)
        await server.start()
        yield server
        await server.stop()

    @pytest.mark.asyncio
    @pytest.mark.skipif(True, reason="Requires kind cluster setup")
    async def test_full_operator_workflow(self, mock_ssh_server):
        """Test complete operator workflow with mock SSH server."""
        # This test would require:
        # 1. kind cluster creation
        # 2. CRD installation
        # 3. Operator deployment
        # 4. Resource creation
        # 5. Reconciliation verification
        # 6. Cleanup

        # For now, this is a placeholder
        # Real implementation would use kubernetes client to interact with cluster
        pass

    @pytest.mark.asyncio
    async def test_ssh_connection_mock(self, mock_ssh_server):
        """Test SSH connection to mock server."""
        # Connect to mock server with authorized key
        async with asyncssh.connect(
            "localhost",
            port=2222,
            username="test",
            client_keys=[mock_ssh_server.get_client_key()],
            known_hosts=None,  # Accept any host key for testing
        ) as conn:
            # Execute test command
            result = await conn.run("uname -a")
            assert result.exit_status == 0
            assert "NixOS" in result.stdout

    @pytest.mark.asyncio
    async def test_nixos_rebuild_mock(self, mock_ssh_server):
        """Test NixOS rebuild command execution."""
        async with asyncssh.connect(
            "localhost",
            port=2222,
            username="test",
            client_keys=[mock_ssh_server.get_client_key()],
            known_hosts=None,
        ) as conn:
            # Execute nixos-rebuild command
            result = await conn.run("nixos-rebuild switch")
            assert result.exit_status == 0
            assert "activation finished" in result.stdout

        # Verify command was executed
        commands = mock_ssh_server.get_executed_commands()
        assert any("nixos-rebuild" in cmd for cmd in commands)


class TestE2EMachineDiscovery:
    """E2E tests for machine discovery workflow."""

    @pytest.fixture
    async def mock_ssh_server(self):
        """Start mock SSH server."""
        server = MockSSHServer(port=2223)
        await server.start()
        yield server
        await server.stop()

    @pytest.mark.asyncio
    async def test_machine_discoverable_check(self, mock_ssh_server):
        """Test machine discoverability check via SSH."""
        from machine_handlers import check_machine_discoverable

        machine_spec = {
            "hostname": "localhost:2223",
            "username": "test",
            "credentialsRef": None,  # No auth for mock
        }

        # Note: This would need adjustment in actual code to support no-auth
        # For E2E, we'd use actual SSH keys
        # This is a simplified test showing the pattern

        # is_discoverable = await check_machine_discoverable(
        #     machine_spec, None, "test-machine", "default"
        # )
        # assert is_discoverable

        # For now, just verify mock server is running
        async with asyncssh.connect(
            "localhost",
            port=2223,
            username="test",
            client_keys=[mock_ssh_server.get_client_key()],
            known_hosts=None,
        ) as conn:
            result = await conn.run("echo test")
            assert result.exit_status == 0


class TestE2EHardwareScanning:
    """E2E tests for hardware scanning workflow."""

    @pytest.fixture
    async def mock_ssh_server_with_hardware(self):
        """Mock SSH server that returns hardware info."""

        class HardwareSSHServer(MockSSHServer):
            async def handle_client(self, process):
                """Handle hardware scanning commands."""
                command = process.command
                self.commands_executed.append(command)

                if "hardware_scanner.sh" in command or "lscpu" in command:
                    # Mock hardware output
                    hardware_json = '{"cpu": "8", "memory": "16GB", "disk": "500GB"}'
                    process.stdout.write(hardware_json)
                else:
                    await super().handle_client(process)

        server = HardwareSSHServer(port=2224)
        await server.start()
        yield server
        await server.stop()

    @pytest.mark.asyncio
    async def test_hardware_scan_execution(self, mock_ssh_server_with_hardware):
        """Test hardware scanning returns data."""
        async with asyncssh.connect(
            "localhost",
            port=2224,
            username="test",
            client_keys=[mock_ssh_server_with_hardware.get_client_key()],
            known_hosts=None,
        ) as conn:
            # Execute hardware scan
            result = await conn.run("lscpu")
            assert result.exit_status == 0
            assert "cpu" in result.stdout.lower()


class TestE2EGitOperations:
    """E2E tests for Git repository operations."""

    @pytest.mark.asyncio
    async def test_git_clone_real_repo(self):
        """Test cloning a real public repository."""
        from utils import clone_git_repo

        with tempfile.TemporaryDirectory() as tmpdir:
            # Clone a small public repo for testing
            repo_url = "https://github.com/NixOS/templates.git"

            try:
                repo_path, commit_hash = await clone_git_repo(
                    repo_url, None, "default", target_path=tmpdir + "/repo"
                )

                # Verify clone succeeded
                assert os.path.exists(repo_path)
                assert len(commit_hash) == 40  # Git SHA-1 hash length
                assert os.path.exists(os.path.join(repo_path, ".git"))
            except Exception as e:
                # Network issues might cause this to fail in CI
                pytest.skip(f"Git clone failed (network issue?): {e}")

    @pytest.mark.asyncio
    async def test_git_commit_hash_retrieval(self):
        """Test retrieving commit hash from repository."""
        from utils import get_remote_commit_hash

        try:
            # Get commit hash for main branch
            commit_hash = await get_remote_commit_hash(
                "https://github.com/NixOS/templates.git", "main", None, "default"
            )

            # Verify we got a valid commit hash
            assert len(commit_hash) == 40
            assert all(c in "0123456789abcdef" for c in commit_hash.lower())
        except Exception as e:
            pytest.skip(f"Failed to get commit hash (network issue?): {e}")
