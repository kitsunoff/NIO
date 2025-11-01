#!/usr/bin/env python3

import logging
import shutil
import asyncio
import tempfile
import kopf
import os
import json
import hashlib
import subprocess
from datetime import datetime
from typing import Dict, Optional, Any

from machine_handlers import check_machine_discoverable
from clients import (
    get_machine,
    update_machine_status,
    update_configuration_status,
    get_secret_data,
)
from utils import (
    clone_git_repo,
    calculate_directory_hash,
    get_workdir_path,
    extract_repo_name_from_url,
    get_remote_commit_hash,
)

logger = logging.getLogger(__name__)


async def inject_additional_files(
    repo_path: str,
    config_spec: Dict[str, Any],
    namespace: str,
    machine_spec: Optional[Dict[str, Any]] = None,
) -> str:
    """Inject additionalFiles into configurationSubdir and return directory hash"""
    if not config_spec.get("additionalFiles"):
        return calculate_directory_hash(repo_path)

    config_subdir = config_spec.get("configurationSubdir", "")
    base_path = os.path.join(repo_path, config_subdir) if config_subdir else repo_path

    injected_files = []  # Store paths of injected files

    for file_spec in config_spec["additionalFiles"]:
        file_path = os.path.join(base_path, file_spec["path"])
        file_dir = os.path.dirname(file_path)

        # Create directory if needed
        os.makedirs(file_dir, exist_ok=True)

        # Handle different value types
        value_type = file_spec["valueType"]

        if value_type == "Inline":
            content = file_spec.get("inline", "")
            with open(file_path, "w") as f:
                f.write(content)
            logger.info(f"Injected inline file: {file_spec['path']}")
            injected_files.append(file_path)

        elif value_type == "SecretRef":
            secret_ref = file_spec.get("secretRef", {})
            secret_name = secret_ref.get("name")
            if not secret_name:
                logger.warning(f"Missing secret name for file {file_spec['path']}")
                continue

            try:
                secret_data = await get_secret_data(secret_name, namespace)
                # Use first key from secret or specified key
                if secret_data:
                    first_key = next(iter(secret_data.keys()))
                    content = secret_data[first_key]
                    with open(file_path, "w") as f:
                        f.write(content)
                    logger.info(
                        f"Injected secret file: {file_spec['path']} from secret {secret_name}"
                    )
                    injected_files.append(file_path)
                else:
                    logger.warning(
                        f"Empty secret {secret_name} for file {file_spec['path']}"
                    )
            except Exception as e:
                logger.error(f"Failed to inject secret file {file_spec['path']}: {e}")

        elif value_type == "NixosFacter":
            if not machine_spec:
                logger.warning(
                    f"Cannot generate NixosFacter for {file_spec['path']}: no machine spec"
                )
                continue

            # Generate NixOS facts
            facts = generate_nixos_facts(machine_spec)
            content = json.dumps(facts, indent=2)
            with open(file_path, "w") as f:
                f.write(content)
            logger.info(f"Generated NixosFacter file: {file_spec['path']}")
            injected_files.append(file_path)

    # Add files to git index without commit
    if injected_files:
        try:
            # Add each file to git index with --intend-to-add
            for file_path in injected_files:
                # Make path relative to repo_path for git command
                rel_path = os.path.relpath(file_path, repo_path)
                subprocess.run(
                    ["git", "add", "--intent-to-add", rel_path],
                    cwd=repo_path,
                    check=True,
                    capture_output=True,
                )
                logger.debug(f"Added to git index (intend-to-add): {rel_path}")

            logger.info(
                f"Added {len(injected_files)} files to git index with --intend-to-add"
            )

        except subprocess.CalledProcessError as e:
            logger.warning(f"Failed to add files to git index: {e}")
            # This is not a critical error - continue working
        except Exception as e:
            logger.warning(f"Unexpected error during git add: {e}")

    # Return directory hash after injection
    return calculate_directory_hash(base_path)


def generate_nixos_facts(machine_spec: Dict[str, Any]) -> Dict[str, Any]:
    """Generate NixOS facts for machine"""
    facts = {
        "machine-id": machine_spec.get("hostname", "unknown"),
        "hostname": machine_spec.get("hostname", "unknown"),
        "ip-address": machine_spec.get("ipAddress", "unknown"),
    }

    # Add hardware facts if available
    if machine_spec.get("status", {}).get("hardwareFacts"):
        facts.update(machine_spec["status"]["hardwareFacts"])

    return facts


def get_additional_files_hash(
    config_spec: Dict[str, Any], namespace: str, machine_spec: Optional[dict] = None
) -> str:
    """Calculate hash from additionalFiles specification"""
    if not config_spec.get("additionalFiles"):
        return ""

    # Generate temporary hash based on additionalFiles content
    files_content = []
    for file_spec in config_spec["additionalFiles"]:
        file_info = {
            "path": file_spec.get("path", ""),
            "valueType": file_spec.get("valueType", ""),
        }

        if file_spec.get("valueType") == "Inline":
            file_info["inline"] = file_spec.get("inline", "")
        elif file_spec.get("valueType") == "SecretRef":
            file_info["secretRef"] = file_spec.get("secretRef", {})
        elif file_spec.get("valueType") == "NixosFacter":
            if machine_spec:
                file_info["nixosFacter"] = generate_nixos_facts(machine_spec)

        files_content.append(file_info)

    content_str = json.dumps(files_content, sort_keys=True)
    return hashlib.sha256(content_str.encode("utf-8")).hexdigest()


# ...
async def apply_nixos_configuration(
    machine_spec: Dict[str, Any],
    config_spec: Dict[str, Any],
    repo_path: str,
    commit_hash: str,
    is_remove: bool,
    needs_full_install: bool,
) -> bool:
    """Apply NixOS configuration to machine. SSH key is optional."""
    tmp_key_path: Optional[str] = None
    try:
        # --- SSH key handling (optional) ---
        ssh_key_ref = machine_spec.get("sshKeySecretRef")
        nix_sshopts = ""  # For nixos-rebuild
        identity_arg_anywhere = ""  # For nixos-anywhere

        if ssh_key_ref:
            secret_name = ssh_key_ref["name"]
            secret_namespace = ssh_key_ref.get(
                "namespace", machine_spec.get("namespace", "default")
            )

            try:
                secret_data = await get_secret_data(secret_name, secret_namespace)
            except Exception as e:
                logger.error(
                    f"Failed to fetch SSH key secret '{secret_name}' in namespace '{secret_namespace}': {e}"
                )
                return False

            ssh_private_key = secret_data.get("ssh-privatekey")
            if not ssh_private_key:
                logger.error(
                    f"Secret '{secret_name}' does not contain 'ssh-privatekey' key"
                )
                return False

            # SECURITY: Save to temporary file in memory-backed tmpfs
            # This prevents keys from persisting on disk after crashes
            shm_dir = "/dev/shm/nio-nix-keys"
            os.makedirs(shm_dir, mode=0o700, exist_ok=True)

            with tempfile.NamedTemporaryFile(
                mode="w", prefix="ssh_key_", delete=False, dir=shm_dir
            ) as tmp:
                tmp.write(ssh_private_key.strip() + "\n")
                tmp_key_path = tmp.name
            # Owner read-only for additional security
            os.chmod(tmp_key_path, 0o400)

            # Form NIX_SSHOPTS for nixos-rebuild (host keys verified via known_hosts)
            nix_sshopts = f"-i {tmp_key_path}"
            # Form argument for nixos-anywhere
            identity_arg_anywhere = f"-i {tmp_key_path}"

        # --- Prepare parameters ---
        config_path = (
            f"{repo_path}/{config_spec['configurationSubdir']}"
            if config_spec.get("configurationSubdir")
            else repo_path
        )
        ssh_user = machine_spec["sshUser"]
        target_host = machine_spec.get("ipAddress") or machine_spec["hostname"]

        # Determine flake
        if is_remove and config_spec.get("onRemoveFlake"):
            flake = config_spec["onRemoveFlake"]
        else:
            flake = config_spec["flake"]

        base_nix = "nix --extra-experimental-features 'nix-command flakes'"

        # For updates use nixos-rebuild even if fullInstall was done before
        if needs_full_install and not is_remove:
            # nixos-anywhere only for first time
            cmd_parts = [
                base_nix,
                "run github:nix-community/nixos-anywhere --",
                f"--target-host {ssh_user}@{target_host}",
                f"--flake {config_path}{flake}",
            ]
            # Add key argument for nixos-anywhere if present
            if identity_arg_anywhere:
                cmd_parts.append(identity_arg_anywhere)
            cmd = " ".join(cmd_parts)
            # For nixos-anywhere NIX_SSHOPTS may not be used, pass through arguments
            env_for_cmd = (
                os.environ.copy()
            )  # nixos-anywhere may not read NIX_SSHOPTS
        else:
            # nixos-rebuild for updates
            # Add NIX_SSHOPTS to shell command via env
            env_for_cmd = os.environ.copy()
            if nix_sshopts:
                env_for_cmd["NIX_SSHOPTS"] = nix_sshopts

            cmd_parts = [
                base_nix,  # NIX_SSHOPTS passed through env
                "shell nixpkgs#nixos-rebuild --command",
                "nixos-rebuild switch",
                f"--flake {config_path}{flake}",
                f"--target-host {ssh_user}@{target_host}",
            ]
            # Don't pass --ssh-option IdentityFile, using NIX_SSHOPTS instead
            cmd = " ".join(cmd_parts)

        logger.info(f"Executing command: {cmd}")

        # Pass env so NIX_SSHOPTS is available for nixos-rebuild
        # nixos-anywhere will get key through argument
        process = await asyncio.create_subprocess_shell(
            cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=env_for_cmd,  # Pass modified environment
        )

        # Async reading of stdout and stderr in real time
        async def read_stream(stream, log_func):
            if stream:
                async for line in stream:
                    decoded = line.decode("utf-8", errors="replace").rstrip()
                    if decoded:
                        log_func(decoded)

        # Start reading stdout and stderr in parallel with timeout (60 minutes for long operations)
        try:
            await asyncio.wait_for(
                asyncio.gather(
                    read_stream(process.stdout, logger.info),
                    read_stream(process.stderr, logger.error),
                    process.wait(),  # wait for process completion
                ),
                timeout=3600  # 60 minutes timeout
            )
        except asyncio.TimeoutError:
            logger.error(f"Command timed out after 60 minutes: {cmd}")
            process.kill()
            await process.wait()
            return False

        if process.returncode != 0:
            logger.error(f"Command failed (code {process.returncode})")
            return False

        logger.info("NixOS configuration applied successfully")
        return True
    except Exception as e:
        logger.error(f"Unexpected error in apply_nixos_configuration: {e}")
        return False

    finally:
        if tmp_key_path and os.path.exists(tmp_key_path):
            os.unlink(tmp_key_path)


# ...


async def reconcile_nixos_configuration(
    body: Dict[str, Any], spec: Dict[str, Any], name: str, namespace: str, **kwargs
) -> None:
    """
    Main reconciliation point for NixosConfiguration.

    Refactored into smaller, focused functions for better maintainability.
    """
    logger.info(f"Reconciling NixosConfiguration: {name}")

    # Import helper functions
    from reconcile_helpers import (
        check_machine_availability,
        prepare_git_repository,
        detect_configuration_changes,
        apply_and_update_status,
        cleanup_repository,
    )

    try:
        deletion_timestamp = body.get("metadata", {}).get("deletionTimestamp")

        # Step 1: Check machine availability
        is_available, machine = await check_machine_availability(spec, name, namespace)
        if not is_available:
            return

        machine_name = spec["machineRef"]["name"]

        # Step 2: Prepare Git repository
        repo_path, actual_commit_hash, workdir_path = await prepare_git_repository(
            spec, name, namespace
        )

        try:
            # Step 3: Calculate hashes and detect changes
            additional_files_hash = get_additional_files_hash(
                spec, namespace, machine["spec"]
            )

            should_reconcile, commit_changed, files_changed = detect_configuration_changes(
                body, spec, actual_commit_hash, additional_files_hash, deletion_timestamp
            )

            # Handle deletion without onRemoveFlake
            if deletion_timestamp and not spec.get("onRemoveFlake"):
                logger.info(f"Deletion without onRemoveFlake for {name}, cleaning up")
                await update_machine_status(
                    machine_name,
                    namespace,
                    {
                        "hasConfiguration": False,
                        "appliedConfiguration": None,
                        "appliedCommit": None,
                    },
                )
                return

            if not should_reconcile:
                logger.info(f"No changes detected, skipping reconcile for {name}")
                return

            # Step 4: Inject additional files
            config_hash = await inject_additional_files(
                repo_path, spec, namespace, machine["spec"]
            )

            # Step 5: Determine if full install is needed
            current_status = body.get("status", {})
            current_has_full_install = current_status.get("fullDiskInstallCompleted", False)
            needs_full_install = spec.get("fullInstall", False) and not current_has_full_install

            # Step 6: Apply configuration and update statuses
            success = await apply_and_update_status(
                name,
                namespace,
                machine_name,
                machine["spec"],
                spec,
                repo_path,
                actual_commit_hash,
                config_hash,
                additional_files_hash,
                deletion_timestamp,
                needs_full_install,
                current_has_full_install,
            )

            if not success:
                raise kopf.TemporaryError("Failed to apply configuration", delay=60)

        finally:
            # Step 7: Cleanup
            await cleanup_repository(repo_path, namespace, name, workdir_path)

    except Exception as e:
        logger.error(f"Failed to reconcile NixosConfiguration {name}: {e}", exc_info=True)
        raise kopf.TemporaryError(f"Configuration reconciliation failed: {e}", delay=60)


async def garbage_collect_old_versions(namespace: str, name: str, current_path: str) -> None:
    """Remove old configuration versions, keeping only current one"""
    base_dir = os.path.dirname(current_path)
    if not os.path.exists(base_dir):
        return

    for item in os.listdir(base_dir):
        item_path = os.path.join(base_dir, item)
        if item_path != current_path and os.path.isdir(item_path):
            try:
                shutil.rmtree(item_path)
                logger.info(f"Garbage collected old configuration: {item_path}")
            except Exception as e:
                logger.warning(f"Failed to garbage collect {item_path}: {e}")


@kopf.on.create("nio.homystack.com", "v1alpha1", "nixosconfigurations")
@kopf.on.update("nio.homystack.com", "v1alpha1", "nixosconfigurations")
@kopf.on.resume("nio.homystack.com", "v1alpha1", "nixosconfigurations")
@kopf.on.delete("nio.homystack.com", "v1alpha1", "nixosconfigurations")
async def unified_nixos_configuration_handler(
    body: Dict[str, Any], spec: Dict[str, Any], name: str, namespace: str, **kwargs
) -> None:
    """Unified handler for all NixosConfiguration operations"""
    await reconcile_nixos_configuration(body, spec, name, namespace, **kwargs)


@kopf.timer("nio.homystack.com", "v1alpha1", "nixosconfigurations", interval=3600.0)
async def garbage_collect_all_old_configurations(**kwargs) -> None:
    """Background GC for all configurations older than 24 hours"""
    base_path = "/tmp/nixos-config"
    if not os.path.exists(base_path):
        return

    current_time = datetime.now().timestamp()

    for namespace in os.listdir(base_path):
        namespace_path = os.path.join(base_path, namespace)
        if not os.path.isdir(namespace_path):
            continue

        for config_name in os.listdir(namespace_path):
            config_path = os.path.join(namespace_path, config_name)
            if not os.path.isdir(config_path):
                continue

            for version_dir in os.listdir(config_path):
                version_path = os.path.join(config_path, version_dir)
                if not os.path.isdir(version_path):
                    continue

                # Check modification time
                try:
                    stat = os.stat(version_path)
                    age_hours = (current_time - stat.st_mtime) / 3600

                    if age_hours > 24:
                        shutil.rmtree(version_path)
                        logger.info(
                            f"GC: Removed old configuration {version_path} (age: {age_hours:.1f}h)"
                        )
                except Exception as e:
                    logger.warning(f"GC: Failed to remove {version_path}: {e}")
