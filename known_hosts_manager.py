#!/usr/bin/env python3

"""
SSH known_hosts management for secure host verification.

This module provides utilities for managing SSH host keys and preventing
Man-in-the-Middle attacks by properly verifying host identities.
"""

import logging
import os
import tempfile
from typing import Optional, Dict, Any
from pathlib import Path

logger = logging.getLogger(__name__)


class KnownHostsManager:
    """Manages SSH known_hosts for host verification"""

    def __init__(self, storage_path: Optional[str] = None):
        """
        Initialize known hosts manager.

        Args:
            storage_path: Path to store known_hosts file. If None, uses temp directory.
        """
        if storage_path:
            self.known_hosts_path = Path(storage_path)
        else:
            # Use persistent temp directory for operator lifetime
            temp_dir = Path("/tmp/nio-ssh-known-hosts")
            temp_dir.mkdir(parents=True, exist_ok=True)
            self.known_hosts_path = temp_dir / "known_hosts"

        # Create file if it doesn't exist
        self.known_hosts_path.touch(mode=0o600, exist_ok=True)
        logger.info(f"Using known_hosts file: {self.known_hosts_path}")

    def get_known_hosts_path(self) -> str:
        """Get path to known_hosts file"""
        return str(self.known_hosts_path)

    def add_host_key(self, hostname: str, key_type: str, public_key: str) -> None:
        """
        Add a host key to known_hosts.

        Args:
            hostname: Hostname or IP address
            key_type: Key type (e.g., 'ssh-ed25519', 'ecdsa-sha2-nistp256')
            public_key: Base64-encoded public key
        """
        entry = f"{hostname} {key_type} {public_key}\n"

        # Check if entry already exists
        if self.known_hosts_path.exists():
            with open(self.known_hosts_path, "r") as f:
                if entry in f.read():
                    logger.debug(f"Host key for {hostname} already in known_hosts")
                    return

        # Append new entry
        with open(self.known_hosts_path, "a") as f:
            f.write(entry)
        logger.info(f"Added host key for {hostname} to known_hosts")

    def trust_on_first_use(self, hostname: str, port: int = 22) -> bool:
        """
        Implement Trust On First Use (TOFU) policy.

        On first connection, accept and store the host key.
        On subsequent connections, verify against stored key.

        Args:
            hostname: Hostname or IP to connect to
            port: SSH port (default 22)

        Returns:
            True if this is first connection (key will be added),
            False if key already exists (will be verified)
        """
        # Check if we have a key for this host
        if not self.known_hosts_path.exists():
            logger.info(f"TOFU: First connection to {hostname}, will trust host key")
            return True

        with open(self.known_hosts_path, "r") as f:
            content = f.read()
            # Simple check - does hostname appear in known_hosts?
            if hostname in content or f"[{hostname}]:{port}" in content:
                logger.debug(f"TOFU: Found existing key for {hostname}")
                return False

        logger.info(f"TOFU: First connection to {hostname}, will trust host key")
        return True

    def clear_host(self, hostname: str) -> None:
        """
        Remove all entries for a specific host.

        Useful when host keys change (e.g., after machine reinstall).

        Args:
            hostname: Hostname to remove
        """
        if not self.known_hosts_path.exists():
            return

        with open(self.known_hosts_path, "r") as f:
            lines = f.readlines()

        # Filter out lines containing this hostname
        filtered_lines = [
            line for line in lines if not line.startswith(hostname + " ") and not line.startswith(f"[{hostname}]:")
        ]

        with open(self.known_hosts_path, "w") as f:
            f.writelines(filtered_lines)

        logger.info(f"Removed host keys for {hostname}")


# Global instance for operator lifetime
_known_hosts_manager: Optional[KnownHostsManager] = None


def get_known_hosts_manager() -> KnownHostsManager:
    """Get or create global known_hosts manager instance"""
    global _known_hosts_manager
    if _known_hosts_manager is None:
        _known_hosts_manager = KnownHostsManager()
    return _known_hosts_manager
