#!/usr/bin/env python3

"""Unit tests for utility functions."""

import pytest
import tempfile
import os
import shutil
from utils import (
    extract_repo_name_from_url,
    calculate_directory_hash,
    get_workdir_path,
    parse_flake_reference,
)


class TestExtractRepoNameFromUrl:
    """Tests for repository name extraction."""

    def test_https_url(self):
        """Extract repo name from HTTPS URL."""
        url = "https://github.com/owner/repo.git"
        result = extract_repo_name_from_url(url)
        assert result == "owner/repo"

    def test_https_url_without_git(self):
        """Extract repo name from HTTPS URL without .git."""
        url = "https://github.com/owner/repo"
        result = extract_repo_name_from_url(url)
        assert result == "owner/repo"

    def test_ssh_url(self):
        """Extract repo name from SSH URL."""
        url = "git@github.com:owner/repo.git"
        result = extract_repo_name_from_url(url)
        assert result == "owner/repo"


class TestCalculateDirectoryHash:
    """Tests for directory hash calculation."""

    def test_empty_directory(self):
        """Hash of empty directory should be empty string."""
        with tempfile.TemporaryDirectory() as tmpdir:
            result = calculate_directory_hash(tmpdir)
            # Empty directory should produce a hash (SHA256 of empty data)
            assert isinstance(result, str)
            assert len(result) == 64  # SHA256 hex length

    def test_directory_with_files(self):
        """Hash should be deterministic for same content."""
        with tempfile.TemporaryDirectory() as tmpdir:
            # Create some files
            with open(os.path.join(tmpdir, "file1.txt"), "w") as f:
                f.write("content1")
            with open(os.path.join(tmpdir, "file2.txt"), "w") as f:
                f.write("content2")

            hash1 = calculate_directory_hash(tmpdir)
            hash2 = calculate_directory_hash(tmpdir)

            # Hash should be deterministic
            assert hash1 == hash2
            assert len(hash1) == 64  # SHA256 hex length

    def test_different_content_different_hash(self):
        """Different content should produce different hash."""
        with tempfile.TemporaryDirectory() as tmpdir1:
            with tempfile.TemporaryDirectory() as tmpdir2:
                # Create different files in each
                with open(os.path.join(tmpdir1, "file.txt"), "w") as f:
                    f.write("content1")
                with open(os.path.join(tmpdir2, "file.txt"), "w") as f:
                    f.write("content2")

                hash1 = calculate_directory_hash(tmpdir1)
                hash2 = calculate_directory_hash(tmpdir2)

                # Hashes should be different
                assert hash1 != hash2

    def test_nonexistent_directory(self):
        """Nonexistent directory should return empty string."""
        result = calculate_directory_hash("/nonexistent/path")
        assert result == ""


class TestGetWorkdirPath:
    """Tests for working directory path generation."""

    def test_creates_directory(self):
        """get_workdir_path should create directory if it doesn't exist."""
        with tempfile.TemporaryDirectory() as base:
            # Temporarily override BASE_CONFIG_PATH
            import config
            original = config.BASE_CONFIG_PATH
            try:
                config.BASE_CONFIG_PATH = base
                path = get_workdir_path("test-ns", "test-config", "owner/repo", "abc123")

                # Path should be created
                assert os.path.exists(path)
                assert "test-ns" in path
                assert "test-config" in path
                assert "owner/repo" in path
                assert "abc123" in path
            finally:
                config.BASE_CONFIG_PATH = original

    def test_path_format(self):
        """Working directory path should follow expected format."""
        with tempfile.TemporaryDirectory() as base:
            import config
            original = config.BASE_CONFIG_PATH
            try:
                config.BASE_CONFIG_PATH = base
                path = get_workdir_path("ns", "name", "owner/repo", "commit")

                # Should contain all components
                assert base in path
                assert path.endswith("owner/repo@commit")
            finally:
                config.BASE_CONFIG_PATH = original


class TestParseFlakeReference:
    """Tests for flake reference parsing."""

    def test_github_flake(self):
        """Parse github: flake reference."""
        ref = "github:owner/repo#hostname"
        repo_name, repo_url, commit = parse_flake_reference(ref)

        assert repo_name == "owner/repo"
        assert repo_url == "https://github.com/owner/repo.git"
        assert commit == "floating"  # No specific commit

    def test_github_flake_with_ref(self):
        """Parse github: flake with branch/tag reference."""
        ref = "github:owner/repo/v1.0#hostname"
        repo_name, repo_url, commit = parse_flake_reference(ref)

        assert repo_name == "owner/repo"
        assert repo_url == "https://github.com/owner/repo.git"
        assert commit == "floating"  # Branch/tag, not commit

    def test_github_flake_with_commit(self):
        """Parse github: flake with commit hash."""
        commit_hash = "a" * 40  # 40-char commit hash
        ref = f"github:owner/repo/{commit_hash}#hostname"
        repo_name, repo_url, commit = parse_flake_reference(ref)

        assert repo_name == "owner/repo"
        assert repo_url == "https://github.com/owner/repo.git"
        assert commit == commit_hash

    def test_local_flake(self):
        """Parse local flake reference."""
        ref = ".#hostname"
        repo_name, repo_url, commit = parse_flake_reference(ref)

        assert repo_name == "local"
        assert repo_url == "."
        assert commit == "local"

    def test_unknown_flake_source(self):
        """Parse unknown flake source."""
        ref = "unknown:some/path#hostname"
        repo_name, repo_url, commit = parse_flake_reference(ref)

        assert repo_name == "unknown"
        assert repo_url == "unknown:some/path"
        assert commit == "unknown"


class TestExtractRepoNameEdgeCases:
    """Tests for repository name extraction edge cases."""

    def test_http_url(self):
        """Extract from HTTP URL (not HTTPS)."""
        url = "http://gitlab.com/owner/repo.git"
        result = extract_repo_name_from_url(url)
        assert result == "owner/repo"

    def test_ssh_url_without_git_suffix(self):
        """Extract from SSH URL without .git suffix."""
        url = "git@github.com:owner/repo"
        result = extract_repo_name_from_url(url)
        assert result == "owner/repo"

    def test_url_with_subdirectories(self):
        """Extract from URL with more than 2 path components."""
        url = "https://example.com/group/subgroup/owner/repo.git"
        result = extract_repo_name_from_url(url)
        # Should extract last two components
        assert result == "owner/repo"
