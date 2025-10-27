#!/usr/bin/env python3

import git
import tempfile
import shutil
import urllib.parse
import os
import re
import hashlib
from typing import Dict, Optional, Tuple, List
from datetime import datetime

from clients import get_secret_data


def get_workdir_path(
    namespace: str, name: str, repo_name: str, commit_hash: str
) -> str:
    """Get predictable working directory path"""
    base_path = "/tmp/nixos-config"
    workdir = f"{base_path}/{namespace}/{name}/{repo_name}@{commit_hash}"
    os.makedirs(workdir, exist_ok=True)
    return workdir


def parse_flake_reference(flake_ref: str) -> Tuple[str, str, str]:
    """
    Parse flake reference and return (repo_name, repo_url, commit_hash)

    Supported formats:
    - github:owner/repo#host
    - github:owner/repo/v1.0#host
    - github:owner/repo/abcdef123456#host
    - .#host (local)
    """
    if flake_ref.startswith("."):
        # Local flake
        return "local", ".", "local"

    # Extract part before #
    flake_parts = flake_ref.split("#", 1)
    flake_source = flake_parts[0]

    # Parse source
    if flake_source.startswith("github:"):
        # github:owner/repo or github:owner/repo/ref
        parts = flake_source[7:].split("/")
        owner = parts[0]
        repo = parts[1]
        repo_name = f"{owner}/{repo}"

        # Determine ref (branch/tag/commit)
        if len(parts) > 2:
            ref = parts[2]
            # Check if ref is a commit (40-character hash)
            if re.match(r"^[a-f0-9]{40}$", ref):
                commit_hash = ref
            else:
                # This is a branch or tag - commit will be determined later
                commit_hash = "floating"
        else:
            # Default to main branch
            commit_hash = "floating"

        repo_url = f"https://github.com/{owner}/{repo}.git"
        return repo_name, repo_url, commit_hash

    # For other sources, parsing can be added
    return "unknown", flake_source, "unknown"


def extract_repo_name_from_url(git_url: str) -> str:
    """Extract repository name from Git URL"""
    # Remove protocol and .git
    clean_url = re.sub(r"^https?://", "", git_url)
    clean_url = re.sub(r"\.git$", "", clean_url)

    # Extract owner/repo
    parts = clean_url.split("/")
    if len(parts) >= 2:
        return f"{parts[-2]}/{parts[-1]}"

    return clean_url


def calculate_directory_hash(directory_path: str) -> str:
    """Calculate SHA256 hash of directory contents"""
    if not os.path.exists(directory_path):
        return ""

    hash_obj = hashlib.sha256()

    for root, dirs, files in os.walk(directory_path):
        # Sort for determinism
        dirs.sort()
        files.sort()

        for file in files:
            file_path = os.path.join(root, file)
            # Add relative path to hash
            rel_path = os.path.relpath(file_path, directory_path)
            hash_obj.update(rel_path.encode("utf-8"))

            # Add file content
            try:
                with open(file_path, "rb") as f:
                    while chunk := f.read(8192):
                        hash_obj.update(chunk)
            except Exception:
                # If file cannot be read, skip it
                pass

    return hash_obj.hexdigest()


async def clone_git_repo(
    git_url: str,
    credentials_ref: Optional[Dict] = None,
    namespace: str = "default",
    target_path: Optional[str] = None,
) -> Tuple[str, str]:
    """Clone Git repository and return path and commit hash"""
    if target_path:
        work_dir = target_path
        # If directory already exists, use it
        if os.path.exists(work_dir):
            try:
                repo = git.Repo(work_dir)
                commit_hash = repo.head.commit.hexsha
                return work_dir, commit_hash
            except Exception:
                # If repository is corrupted, remove and clone again
                shutil.rmtree(work_dir, ignore_errors=True)
    else:
        # Old mode with temporary directories (for backward compatibility)
        work_dir = tempfile.mkdtemp(prefix="nixos-operator-")

    try:
        # Prepare credentials if available
        git_kwargs = {}
        if credentials_ref:
            secret_data = await get_secret_data(credentials_ref["name"], namespace)
            # Assume secret contains ssh-privatekey or token
            if "ssh-privatekey" in secret_data:
                ssh_key = secret_data["ssh-privatekey"]
                git_kwargs["env"] = {"GIT_SSH_COMMAND": f"ssh -i {ssh_key}"}
            elif "token" in secret_data:
                # For HTTPS with token
                parsed_url = urllib.parse.urlparse(git_url)
                auth_url = f"{parsed_url.scheme}://token:{secret_data['token']}@{parsed_url.netloc}{parsed_url.path}"
                git_url = auth_url

        # Clone repository
        repo = git.Repo.clone_from(git_url, work_dir, **git_kwargs)
        commit_hash = repo.head.commit.hexsha

        return work_dir, commit_hash

    except Exception as e:
        if not target_path:  # Remove only temporary directories
            shutil.rmtree(work_dir, ignore_errors=True)
        raise


async def get_remote_commit_hash(
    git_url: str,
    ref: str,
    credentials_ref: Optional[Dict] = None,
    namespace: str = "default",
) -> str:
    """Get commit hash for specified branch/tag from remote repository"""
    try:
        # Create temporary directory for ls-remote
        temp_dir = tempfile.mkdtemp(prefix="nixos-operator-lsremote-")

        try:
            # Prepare credentials if available
            git_kwargs = {}
            if credentials_ref:
                secret_data = await get_secret_data(credentials_ref["name"], namespace)
                if "ssh-privatekey" in secret_data:
                    ssh_key = secret_data["ssh-privatekey"]
                    git_kwargs["env"] = {"GIT_SSH_COMMAND": f"ssh -i {ssh_key}"}
                elif "token" in secret_data:
                    parsed_url = urllib.parse.urlparse(git_url)
                    auth_url = f"{parsed_url.scheme}://token:{secret_data['token']}@{parsed_url.netloc}{parsed_url.path}"
                    git_url = auth_url

            # Execute git ls-remote
            repo = git.Repo.init(temp_dir)
            origin = repo.create_remote("origin", git_url)
            origin.fetch(**git_kwargs)

            # Search for ref
            for ref_info in repo.git.ls_remote(git_url, ref).split("\n"):
                if ref_info:
                    parts = ref_info.split()
                    if len(parts) >= 2:
                        return parts[0]  # Commit hash

            raise Exception(f"Ref '{ref}' not found in repository")

        finally:
            shutil.rmtree(temp_dir, ignore_errors=True)

    except Exception as e:
        raise Exception(f"Failed to get remote commit hash for {ref}: {e}")
