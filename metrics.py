#!/usr/bin/env python3

"""
Prometheus metrics for the NixOS Infrastructure Operator.

Provides observability into operator health, performance, and resource states.
"""

import logging
from prometheus_client import Counter, Gauge, Histogram, Info

logger = logging.getLogger(__name__)

# Info metrics
operator_info = Info("nio_operator", "NixOS Infrastructure Operator information")

# Machine metrics
machines_total = Gauge(
    "nio_machines_total",
    "Total number of managed machines",
    ["namespace"],
)

machines_discoverable = Gauge(
    "nio_machines_discoverable",
    "Number of discoverable machines",
    ["namespace"],
)

machines_with_configuration = Gauge(
    "nio_machines_with_configuration",
    "Number of machines with applied configuration",
    ["namespace"],
)

# Configuration metrics
configurations_total = Gauge(
    "nio_configurations_total",
    "Total number of NixOS configurations",
    ["namespace"],
)

configurations_applied = Counter(
    "nio_configurations_applied_total",
    "Total number of successful configuration applications",
    ["namespace", "machine"],
)

configurations_failed = Counter(
    "nio_configurations_failed_total",
    "Total number of failed configuration applications",
    ["namespace", "machine", "reason"],
)

# Reconciliation metrics
reconcile_duration = Histogram(
    "nio_reconcile_duration_seconds",
    "Time spent reconciling configurations",
    ["namespace", "configuration"],
    buckets=(1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600),  # Up to 1 hour
)

reconcile_errors = Counter(
    "nio_reconcile_errors_total",
    "Total number of reconciliation errors",
    ["namespace", "configuration", "error_type"],
)

# SSH connection metrics
ssh_connections_total = Counter(
    "nio_ssh_connections_total",
    "Total number of SSH connection attempts",
    ["namespace", "machine", "result"],
)

ssh_connection_duration = Histogram(
    "nio_ssh_connection_duration_seconds",
    "Time to establish SSH connections",
    ["namespace", "machine"],
    buckets=(0.1, 0.5, 1, 2, 5, 10, 30),
)

# Git operations metrics
git_clones_total = Counter(
    "nio_git_clones_total",
    "Total number of Git clone operations",
    ["namespace", "repository", "result"],
)

git_clone_duration = Histogram(
    "nio_git_clone_duration_seconds",
    "Time to clone Git repositories",
    ["namespace", "repository"],
    buckets=(1, 5, 10, 30, 60, 120, 300),
)

# NixOS operations metrics
nixos_builds_total = Counter(
    "nio_nixos_builds_total",
    "Total number of NixOS builds",
    ["namespace", "machine", "build_type", "result"],
)

nixos_build_duration = Histogram(
    "nio_nixos_build_duration_seconds",
    "Time to build and apply NixOS configurations",
    ["namespace", "machine", "build_type"],
    buckets=(60, 300, 600, 1200, 1800, 3600, 7200),  # Up to 2 hours
)

# Retry metrics
retries_total = Counter(
    "nio_retries_total",
    "Total number of operation retries",
    ["operation", "attempt"],
)

retries_exhausted = Counter(
    "nio_retries_exhausted_total",
    "Total number of operations that exhausted all retries",
    ["operation"],
)

# Error metrics
errors_total = Counter(
    "nio_errors_total",
    "Total number of errors by type",
    ["error_type", "component"],
)

# Validation metrics
validation_errors = Counter(
    "nio_validation_errors_total",
    "Total number of input validation errors",
    ["validation_type", "field"],
)


def init_metrics():
    """Initialize metrics with operator information"""
    operator_info.info(
        {
            "version": "0.2.0",
            "name": "nixos-infrastructure-operator",
            "component": "operator",
        }
    )
    logger.info("Prometheus metrics initialized")


# Helper functions for common metric operations
def record_reconcile_success(namespace: str, configuration: str, duration: float):
    """Record successful reconciliation"""
    reconcile_duration.labels(namespace=namespace, configuration=configuration).observe(duration)


def record_reconcile_error(namespace: str, configuration: str, error_type: str):
    """Record reconciliation error"""
    reconcile_errors.labels(
        namespace=namespace, configuration=configuration, error_type=error_type
    ).inc()


def record_ssh_connection(namespace: str, machine: str, success: bool, duration: float):
    """Record SSH connection attempt"""
    result = "success" if success else "failure"
    ssh_connections_total.labels(namespace=namespace, machine=machine, result=result).inc()
    if success:
        ssh_connection_duration.labels(namespace=namespace, machine=machine).observe(duration)


def record_git_clone(namespace: str, repository: str, success: bool, duration: float):
    """Record Git clone operation"""
    result = "success" if success else "failure"
    git_clones_total.labels(namespace=namespace, repository=repository, result=result).inc()
    if success:
        git_clone_duration.labels(namespace=namespace, repository=repository).observe(duration)


def record_nixos_build(
    namespace: str, machine: str, build_type: str, success: bool, duration: float
):
    """Record NixOS build operation"""
    result = "success" if success else "failure"
    nixos_builds_total.labels(
        namespace=namespace, machine=machine, build_type=build_type, result=result
    ).inc()
    if success:
        nixos_build_duration.labels(
            namespace=namespace, machine=machine, build_type=build_type
        ).observe(duration)
