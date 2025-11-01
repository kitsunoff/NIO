#!/usr/bin/env python3

"""
Helper functions for NixOS configuration reconciliation.

This module contains smaller, focused functions that were extracted from the
original giant reconcile_nixos_configuration function for better maintainability,
testability, and code clarity.
"""

import logging
import shutil
from datetime import datetime
from typing import Dict, Optional, Tuple, Any

from machine_handlers import check_machine_discoverable
from clients import get_machine, update_configuration_status
from utils import (
    clone_git_repo,
    get_workdir_path,
    extract_repo_name_from_url,
    get_remote_commit_hash,
)
from nixosconfiguration_handlers import (
    inject_additional_files,
    get_additional_files_hash,
    apply_nixos_configuration,
    garbage_collect_old_versions,
)
from retry_utils import with_retry

logger = logging.getLogger(__name__)


async def check_machine_availability(
    spec: Dict[str, Any], name: str, namespace: str
) -> Tuple[bool, Optional[Dict[str, Any]]]:
    """
    Check if target machine is available and return machine spec.

    Args:
        spec: NixosConfiguration spec
        name: Configuration name
        namespace: Kubernetes namespace

    Returns:
        Tuple of (is_available, machine_spec)
    """
    machine_name = spec["machineRef"]["name"]
    machine = get_machine(machine_name, namespace)

    is_discoverable = await check_machine_discoverable(
        machine["spec"], None, machine_name, namespace
    )

    if not is_discoverable:
        logger.warning(
            f"Machine {machine_name} is not discoverable for configuration {name}"
        )
        await update_configuration_status(
            name,
            namespace,
            {
                "appliedCommit": None,
                "lastAppliedTime": None,
                "targetMachine": machine_name,
                "conditions": [
                    {
                        "type": "Applied",
                        "status": "False",
                        "lastTransitionTime": datetime.utcnow().isoformat() + "Z",
                        "reason": "MissingCredentials",
                        "message": "Configuration application skipped due to missing SSH credentials",
                    }
                ],
            },
        )
        return False, None

    return True, machine


@with_retry(max_attempts=3, initial_delay=2.0, max_delay=30.0)
async def prepare_git_repository(
    spec: Dict[str, Any], name: str, namespace: str
) -> Tuple[str, str, str]:
    """
    Clone and prepare Git repository for configuration.

    Includes retry logic for transient network failures.

    Args:
        spec: NixosConfiguration spec
        name: Configuration name
        namespace: Kubernetes namespace

    Returns:
        Tuple of (repo_path, actual_commit_hash, workdir_path)
    """
    repo_url = spec["gitRepo"]
    repo_name = extract_repo_name_from_url(repo_url)

    # Get git reference (branch, tag, or commit) - default to "main"
    git_ref = spec.get("ref", "main")

    # Get current commit hash from the repository
    new_commit_hash = await get_remote_commit_hash(
        repo_url, git_ref, spec.get("credentialsRef"), namespace
    )

    # Create predictable path
    workdir_path = get_workdir_path(namespace, name, repo_name, new_commit_hash)

    # Clone repository to predictable path
    repo_path, actual_commit_hash = await clone_git_repo(
        repo_url, spec.get("credentialsRef"), namespace, target_path=workdir_path
    )

    logger.info(
        f"Prepared repository {repo_name} at commit {actual_commit_hash[:8]}"
    )
    return repo_path, actual_commit_hash, workdir_path


def detect_configuration_changes(
    body: Dict[str, Any],
    spec: Dict[str, Any],
    actual_commit_hash: str,
    additional_files_hash: str,
    deletion_timestamp: Optional[str],
) -> Tuple[bool, bool, bool]:
    """
    Detect if configuration has changes that require reconciliation.

    Args:
        body: Full NixosConfiguration resource body
        spec: NixosConfiguration spec
        actual_commit_hash: Current Git commit hash
        additional_files_hash: Hash of additional files
        deletion_timestamp: Deletion timestamp if resource is being deleted

    Returns:
        Tuple of (should_reconcile, commit_changed, additional_files_changed)
    """
    current_status = body.get("status", {})
    current_applied_commit = current_status.get("appliedCommit")
    current_additional_files_hash = current_status.get("additionalFilesHash", "")

    commit_changed = current_applied_commit != actual_commit_hash
    additional_files_changed = current_additional_files_hash != additional_files_hash

    should_reconcile = False

    if deletion_timestamp:
        if spec.get("onRemoveFlake"):
            logger.info(f"Deletion with onRemoveFlake detected, will reconcile")
            should_reconcile = True
        else:
            logger.info(f"Deletion without onRemoveFlake, will skip reconciliation")
    else:
        if commit_changed or additional_files_changed:
            logger.info(
                f"Changes detected - commit: {commit_changed}, "
                f"additionalFiles: {additional_files_changed}"
            )
            should_reconcile = True

    return should_reconcile, commit_changed, additional_files_changed


async def apply_and_update_status(
    name: str,
    namespace: str,
    machine_name: str,
    machine_spec: Dict[str, Any],
    config_spec: Dict[str, Any],
    repo_path: str,
    actual_commit_hash: str,
    config_hash: str,
    additional_files_hash: str,
    deletion_timestamp: Optional[str],
    needs_full_install: bool,
    current_has_full_install: bool,
) -> bool:
    """
    Apply configuration to machine and update resource statuses.

    Args:
        name: Configuration name
        namespace: Kubernetes namespace
        machine_name: Target machine name
        machine_spec: Machine specification
        config_spec: NixosConfiguration specification
        repo_path: Path to cloned repository
        actual_commit_hash: Git commit hash
        config_hash: Configuration directory hash
        additional_files_hash: Additional files hash
        deletion_timestamp: If not None, resource is being deleted
        needs_full_install: Whether full disk install is needed
        current_has_full_install: Whether full install was done before

    Returns:
        True if successful, False otherwise
    """
    from clients import update_machine_status

    # Apply configuration
    success = await apply_nixos_configuration(
        machine_spec,
        config_spec,
        repo_path,
        actual_commit_hash,
        bool(deletion_timestamp),
        needs_full_install,
    )

    if not success:
        return False

    current_time = datetime.utcnow().isoformat() + "Z"

    if deletion_timestamp:
        # On deletion, remove configuration from machine status
        await update_machine_status(
            machine_name,
            namespace,
            {
                "hasConfiguration": False,
                "appliedConfiguration": None,
                "appliedCommit": None,
            },
        )

        await update_configuration_status(
            name,
            namespace,
            {
                "appliedCommit": actual_commit_hash,
                "lastAppliedTime": current_time,
                "targetMachine": machine_name,
                "configurationHash": config_hash,
                "additionalFilesHash": additional_files_hash,
                "hasFullInstall": current_has_full_install,
                "conditions": [
                    {
                        "type": "Applied",
                        "status": "True",
                        "lastTransitionTime": current_time,
                        "reason": "Removed",
                        "message": "Configuration successfully removed",
                    }
                ],
            },
        )
    else:
        # On normal reconciliation, update statuses
        await update_machine_status(
            machine_name,
            namespace,
            {
                "hasConfiguration": True,
                "appliedConfiguration": name,
                "appliedCommit": actual_commit_hash,
                "lastAppliedTime": current_time,
            },
        )

        await update_configuration_status(
            name,
            namespace,
            {
                "appliedCommit": actual_commit_hash,
                "lastAppliedTime": current_time,
                "targetMachine": machine_name,
                "configurationHash": config_hash,
                "additionalFilesHash": additional_files_hash,
                "hasFullInstall": needs_full_install or current_has_full_install,
                "conditions": [
                    {
                        "type": "Applied",
                        "status": "True",
                        "lastTransitionTime": current_time,
                        "reason": "Success",
                        "message": "Configuration successfully applied",
                    }
                ],
            },
        )

    logger.info(f"Successfully reconciled configuration {name} to machine {machine_name}")
    return True


async def cleanup_repository(repo_path: str, namespace: str, name: str, workdir_path: str) -> None:
    """
    Clean up repository and run garbage collection.

    Args:
        repo_path: Path to repository to clean up
        namespace: Kubernetes namespace
        name: Configuration name
        workdir_path: Current working directory path
    """
    try:
        shutil.rmtree(repo_path, ignore_errors=True)
        await garbage_collect_old_versions(namespace, name, workdir_path)
    except Exception as e:
        logger.warning(f"Cleanup failed: {e}")
