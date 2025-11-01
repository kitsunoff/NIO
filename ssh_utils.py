#!/usr/bin/env python3

import logging
import asyncssh
import tempfile
import os
from typing import Dict, Optional, Tuple, Any
from clients import get_secret_data
from events import emit_missing_credentials_event
from known_hosts_manager import get_known_hosts_manager

logger = logging.getLogger(__name__)


async def establish_ssh_connection(
    machine_spec: Dict[str, Any],
    body: Optional[Dict[str, Any]] = None,
    machine_name: Optional[str] = None,
    namespace: Optional[str] = None,
) -> Tuple[Optional[asyncssh.SSHClientConnection], Optional[str]]:
    """
    Establish SSH connection to a machine using key, password, or no authentication.

    Uses Trust On First Use (TOFU) policy for host key verification.

    Returns:
        Tuple of (connection, temp_key_path) where connection is the SSH connection
        and temp_key_path is the path to temporary SSH key file (if created, None otherwise).
        Returns (None, None) if connection fails.
    """
    # Get known_hosts manager for host verification
    known_hosts_mgr = get_known_hosts_manager()

    ssh_config = {
        "host": machine_spec["hostname"],
        "username": machine_spec.get("sshUser", "root"),
        "known_hosts": known_hosts_mgr.get_known_hosts_path(),  # Enable host verification
    }

    has_credentials = False
    ssh_key_temp_file = None

    # Attempt SSH key connection
    if "sshKeySecretRef" in machine_spec:
        try:
            secret_data = await get_secret_data(
                machine_spec["sshKeySecretRef"]["name"],
                machine_spec["sshKeySecretRef"].get("namespace", "default"),
            )
            if "ssh-privatekey" in secret_data and secret_data["ssh-privatekey"]:
                # Create temporary file for SSH key
                with tempfile.NamedTemporaryFile(
                    mode="w", delete=False, suffix="_ssh_key"
                ) as temp_file:
                    temp_file.write(secret_data["ssh-privatekey"])
                    ssh_key_temp_file = temp_file.name

                # Set correct permissions for SSH key
                os.chmod(ssh_key_temp_file, 0o600)

                ssh_config["client_keys"] = [ssh_key_temp_file]
                has_credentials = True
                logger.info("Using SSH key for authentication")
            else:
                # Secret exists but doesn't contain SSH key
                if body:
                    emit_missing_credentials_event(
                        body,
                        "MissingSSHKey",
                        f"Secret {machine_spec['sshKeySecretRef']['name']} exists but doesn't contain 'ssh-privatekey'",
                    )
                logger.warning(
                    f"Secret {machine_spec['sshKeySecretRef']['name']} exists but doesn't contain 'ssh-privatekey'"
                )
        except Exception as e:
            # Secret not found or unavailable
            if body:
                emit_missing_credentials_event(
                    body,
                    "SecretNotFound",
                    f"Failed to get SSH key from secret {machine_spec['sshKeySecretRef']['name']}",
                )
            logger.warning(
                f"Failed to get SSH key from secret {machine_spec['sshKeySecretRef']['name']}: {e}"
            )

    # Attempt password connection (if key didn't work or not specified)
    if not has_credentials and "sshPasswordSecretRef" in machine_spec:
        try:
            secret_data = await get_secret_data(
                machine_spec["sshPasswordSecretRef"]["name"],
                machine_spec["sshPasswordSecretRef"].get("namespace", "default"),
            )

            # Determine password key (default 'password')
            password_key = machine_spec["sshPasswordSecretRef"].get(
                "key", "password"
            )

            if password_key in secret_data and secret_data[password_key]:
                ssh_config["password"] = secret_data[password_key]
                has_credentials = True
                logger.info("Using password for authentication")
            else:
                # Secret exists but doesn't contain password
                if body:
                    emit_missing_credentials_event(
                        body,
                        "MissingPassword",
                        f"Secret {machine_spec['sshPasswordSecretRef']['name']} exists but doesn't contain '{password_key}'",
                    )
                logger.warning(
                    f"Secret {machine_spec['sshPasswordSecretRef']['name']} exists but doesn't contain '{password_key}'"
                )
        except Exception as e:
            # Secret not found or unavailable
            if body:
                emit_missing_credentials_event(
                    body,
                    "SecretNotFound",
                    f"Failed to get password from secret {machine_spec['sshPasswordSecretRef']['name']}",
                )
            logger.warning(
                f"Failed to get password from secret {machine_spec['sshPasswordSecretRef']['name']}: {e}"
            )

    # If no credentials provided, try connection without authentication
    if not has_credentials:
        logger.info(
            "No SSH key or password provided, attempting connection without authentication"
        )

    # Attempt connection
    try:
        conn = await asyncssh.connect(**ssh_config)
        return conn, ssh_key_temp_file
    except Exception as e:
        logger.warning(
            f"Machine {machine_spec.get('hostname')} connection failed: {e}"
        )
        # Clean up temp file if connection failed
        if ssh_key_temp_file and os.path.exists(ssh_key_temp_file):
            try:
                os.unlink(ssh_key_temp_file)
            except Exception as cleanup_error:
                logger.warning(
                    f"Failed to delete temporary SSH key file {ssh_key_temp_file}: {cleanup_error}"
                )
        return None, None


def cleanup_ssh_key(ssh_key_temp_file: Optional[str]) -> None:
    """Clean up temporary SSH key file"""
    if ssh_key_temp_file and os.path.exists(ssh_key_temp_file):
        try:
            os.unlink(ssh_key_temp_file)
            logger.debug(f"Deleted temporary SSH key file: {ssh_key_temp_file}")
        except Exception as e:
            logger.warning(
                f"Failed to delete temporary SSH key file {ssh_key_temp_file}: {e}"
            )
