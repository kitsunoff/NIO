#!/usr/bin/env python3

"""
Configuration module for NixOS Infrastructure Operator.

All configuration values are loaded from environment variables with sensible defaults.
This eliminates hardcoded values and allows runtime configuration via ConfigMaps/Secrets.
"""

import os
from typing import Optional


def get_env_int(key: str, default: int) -> int:
    """Get integer value from environment variable."""
    value = os.environ.get(key)
    if value is None:
        return default
    try:
        return int(value)
    except ValueError:
        raise ValueError(f"Environment variable {key}={value} is not a valid integer")


def get_env_float(key: str, default: float) -> float:
    """Get float value from environment variable."""
    value = os.environ.get(key)
    if value is None:
        return default
    try:
        return float(value)
    except ValueError:
        raise ValueError(f"Environment variable {key}={value} is not a valid float")


def get_env_str(key: str, default: str) -> str:
    """Get string value from environment variable."""
    return os.environ.get(key, default)


# Filesystem paths
BASE_CONFIG_PATH = get_env_str("NIO_BASE_CONFIG_PATH", "/tmp/nixos-config")
KNOWN_HOSTS_PATH = get_env_str("NIO_KNOWN_HOSTS_PATH", "/tmp/nio-ssh-known-hosts")
REMOTE_HARDWARE_SCRIPT_PATH = get_env_str(
    "NIO_REMOTE_HARDWARE_SCRIPT_PATH", "/tmp/hardware_scanner.sh"
)

# Reconciliation intervals (seconds)
MACHINE_DISCOVERY_INTERVAL = get_env_float("NIO_MACHINE_DISCOVERY_INTERVAL", 60.0)
HARDWARE_SCAN_INTERVAL = get_env_float("NIO_HARDWARE_SCAN_INTERVAL", 300.0)
CONFIG_RECONCILE_INTERVAL = get_env_float("NIO_CONFIG_RECONCILE_INTERVAL", 120.0)

# Operation timeouts (seconds)
NIXOS_APPLY_TIMEOUT = get_env_int("NIO_NIXOS_APPLY_TIMEOUT", 3600)

# Retry configuration
RETRY_MAX_ATTEMPTS = get_env_int("NIO_RETRY_MAX_ATTEMPTS", 3)
RETRY_INITIAL_DELAY = get_env_float("NIO_RETRY_INITIAL_DELAY", 2.0)
RETRY_MAX_DELAY = get_env_float("NIO_RETRY_MAX_DELAY", 30.0)
RETRY_EXPONENTIAL_BASE = get_env_float("NIO_RETRY_EXPONENTIAL_BASE", 2.0)

# Metrics and health checks
METRICS_PORT = get_env_int("METRICS_PORT", 8000)
HEALTH_CHECK_PORT = get_env_int("HEALTH_CHECK_PORT", 8080)


def get_config_summary() -> str:
    """Return configuration summary for logging."""
    return f"""NixOS Infrastructure Operator Configuration:
  Paths:
    - Base config path: {BASE_CONFIG_PATH}
    - Known hosts path: {KNOWN_HOSTS_PATH}
    - Remote hardware script: {REMOTE_HARDWARE_SCRIPT_PATH}

  Intervals:
    - Machine discovery: {MACHINE_DISCOVERY_INTERVAL}s
    - Hardware scan: {HARDWARE_SCAN_INTERVAL}s
    - Config reconcile: {CONFIG_RECONCILE_INTERVAL}s

  Timeouts:
    - NixOS apply: {NIXOS_APPLY_TIMEOUT}s

  Retry:
    - Max attempts: {RETRY_MAX_ATTEMPTS}
    - Initial delay: {RETRY_INITIAL_DELAY}s
    - Max delay: {RETRY_MAX_DELAY}s
    - Exponential base: {RETRY_EXPONENTIAL_BASE}

  Observability:
    - Metrics port: {METRICS_PORT}
    - Health check port: {HEALTH_CHECK_PORT}
"""
