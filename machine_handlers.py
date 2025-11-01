#!/usr/bin/env python3

import logging
import os
from typing import Dict, Optional, Any

from ssh_utils import establish_ssh_connection, cleanup_ssh_key
import config

logger = logging.getLogger(__name__)


async def check_machine_discoverable(
    machine_spec: Dict[str, Any],
    body: Optional[Dict[str, Any]] = None,
    machine_name: Optional[str] = None,
    namespace: Optional[str] = None,
) -> bool:
    """Check machine availability via SSH with support for key, password, and no authentication"""
    conn, ssh_key_temp_file = await establish_ssh_connection(
        machine_spec, body, machine_name, namespace
    )

    if not conn:
        return False

    try:
        # Simple command to check availability
        result = await conn.run('echo "machine_available"', check=True)
        return result.stdout.strip() == "machine_available"
    except Exception as e:
        logger.warning(
            f"Machine {machine_spec.get('hostname')} availability check failed: {e}"
        )
        return False
    finally:
        conn.close()
        await conn.wait_closed()
        cleanup_ssh_key(ssh_key_temp_file)


async def scan_machine_hardware(
    machine_spec: Dict[str, Any],
    body: Optional[Dict[str, Any]] = None,
    machine_name: Optional[str] = None,
    namespace: Optional[str] = None,
) -> Dict[str, Any]:
    """Scan machine hardware and return facts"""
    conn, ssh_key_temp_file = await establish_ssh_connection(
        machine_spec, body, machine_name, namespace
    )

    if not conn:
        logger.warning(
            f"Failed to connect to machine {machine_spec.get('hostname')} for hardware scan"
        )
        return {}

    try:
        # Transfer scan script via SCP
        scanner_path = os.path.join(
            os.path.dirname(__file__), "scripts", "hardware_scanner.sh"
        )

        if not os.path.exists(scanner_path):
            logger.error(f"Hardware scanner script not found at {scanner_path}")
            return {}

        # Read script content
        with open(scanner_path, "r") as f:
            scanner_content = f.read()

        # Create temporary file on remote machine
        remote_script_path = config.REMOTE_HARDWARE_SCRIPT_PATH

        # Transfer script via SCP
        async with conn.start_sftp_client() as sftp:
            async with sftp.open(remote_script_path, "w") as remote_file:
                await remote_file.write(scanner_content)

        # Make script executable and run it
        await conn.run(f"chmod +x {remote_script_path}", check=True)
        result = await conn.run(f"{remote_script_path}", check=True)

        # Get raw scanner output
        facts_output = result.stdout.strip()
        if not facts_output:
            logger.warning("Hardware scanner returned empty output")
            return {}

        # Parse result locally
        from scripts.facts_parser import parse_facts

        # Split output into lines and parse
        lines = facts_output.split("\n")
        hardware_facts = parse_facts(lines)

        logger.info(
            f"Successfully scanned hardware for machine {machine_spec['hostname']}"
        )
        return hardware_facts

    except Exception as e:
        logger.warning(
            f"Failed to scan hardware for machine {machine_spec.get('hostname')}: {e}"
        )
        return {}
    finally:
        conn.close()
        await conn.wait_closed()
        cleanup_ssh_key(ssh_key_temp_file)
