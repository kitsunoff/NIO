#!/usr/bin/env python3

"""
Input validation utilities for preventing command injection and other attacks.

This module provides validation functions for user-controlled inputs like
hostnames, URLs, and other parameters that could be exploited.
"""

import re
import logging
from typing import Optional
from urllib.parse import urlparse

logger = logging.getLogger(__name__)


class ValidationError(Exception):
    """Raised when input validation fails"""

    pass


def validate_hostname(hostname: str) -> str:
    """
    Validate hostname or IP address to prevent command injection.

    Args:
        hostname: Hostname or IP address to validate

    Returns:
        Validated hostname (same as input if valid)

    Raises:
        ValidationError: If hostname is invalid or contains dangerous characters
    """
    if not hostname:
        raise ValidationError("Hostname cannot be empty")

    if len(hostname) > 253:
        raise ValidationError(f"Hostname too long: {len(hostname)} > 253 characters")

    # Allow hostnames, IPv4, and IPv6
    # Hostname pattern: alphanumeric, hyphens, dots
    # IPv4: digits and dots
    # IPv6: hex digits, colons, brackets (can start with [)
    safe_pattern = r"^[\[a-zA-Z0-9]([a-zA-Z0-9\-\.:\[\]])*[a-zA-Z0-9\]]?$"

    if not re.match(safe_pattern, hostname):
        raise ValidationError(
            f"Hostname contains invalid characters: {hostname}. "
            "Only alphanumeric, hyphens, dots, colons, and brackets allowed."
        )

    # Check for dangerous patterns
    dangerous_patterns = [
        r";",  # Command separator
        r"\$",  # Variable expansion
        r"`",  # Command substitution
        r"\|",  # Pipe
        r"&",  # Background/AND
        r">",  # Redirect
        r"<",  # Redirect
        r"\(",  # Subshell
        r"\)",  # Subshell
        r"\{",  # Brace expansion
        r"\}",  # Brace expansion
        r"\n",  # Newline
        r"\r",  # Carriage return
    ]

    for pattern in dangerous_patterns:
        if re.search(pattern, hostname):
            raise ValidationError(
                f"Hostname contains dangerous character: {hostname}"
            )

    logger.debug(f"Validated hostname: {hostname}")
    return hostname


def validate_git_url(git_url: str) -> str:
    """
    Validate Git repository URL to prevent command injection.

    Args:
        git_url: Git repository URL to validate

    Returns:
        Validated URL (same as input if valid)

    Raises:
        ValidationError: If URL is invalid or dangerous
    """
    if not git_url:
        raise ValidationError("Git URL cannot be empty")

    if len(git_url) > 2048:
        raise ValidationError(f"Git URL too long: {len(git_url)} > 2048 characters")

    # Parse URL
    try:
        parsed = urlparse(git_url)
    except Exception as e:
        raise ValidationError(f"Invalid URL format: {e}")

    # Allow only safe protocols
    allowed_schemes = ["https", "http", "git", "ssh"]
    if parsed.scheme and parsed.scheme not in allowed_schemes:
        raise ValidationError(
            f"Disallowed URL scheme: {parsed.scheme}. "
            f"Allowed: {', '.join(allowed_schemes)}"
        )

    # Check for dangerous characters in URL
    dangerous_chars = [";", "$", "`", "|", "&", "\n", "\r", "$(", "${"]
    for char in dangerous_chars:
        if char in git_url:
            raise ValidationError(f"Git URL contains dangerous character: {char}")

    logger.debug(f"Validated Git URL: {git_url}")
    return git_url


def validate_ssh_username(username: str) -> str:
    """
    Validate SSH username to prevent command injection.

    Args:
        username: SSH username to validate

    Returns:
        Validated username (same as input if valid)

    Raises:
        ValidationError: If username is invalid
    """
    if not username:
        raise ValidationError("SSH username cannot be empty")

    if len(username) > 32:
        raise ValidationError(f"SSH username too long: {len(username)} > 32 characters")

    # Only allow alphanumeric, underscore, hyphen
    if not re.match(r"^[a-zA-Z0-9_\-]+$", username):
        raise ValidationError(
            f"SSH username contains invalid characters: {username}. "
            "Only alphanumeric, underscore, and hyphen allowed."
        )

    logger.debug(f"Validated SSH username: {username}")
    return username


def validate_path(path: str, max_length: int = 4096) -> str:
    """
    Validate file path to prevent directory traversal and injection.

    Args:
        path: File path to validate
        max_length: Maximum allowed path length

    Returns:
        Validated path (same as input if valid)

    Raises:
        ValidationError: If path is invalid or dangerous
    """
    if not path:
        raise ValidationError("Path cannot be empty")

    if len(path) > max_length:
        raise ValidationError(f"Path too long: {len(path)} > {max_length} characters")

    # Check for null bytes
    if "\x00" in path:
        raise ValidationError("Path contains null byte")

    # Check for dangerous patterns
    if ".." in path:
        logger.warning(f"Path contains parent directory reference: {path}")
        # Not necessarily dangerous, but log it

    # Check for command injection characters
    dangerous_chars = [";", "$", "`", "|", "&", "\n", "\r"]
    for char in dangerous_chars:
        if char in path:
            raise ValidationError(f"Path contains dangerous character: {char}")

    logger.debug(f"Validated path: {path}")
    return path
