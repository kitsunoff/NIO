#!/usr/bin/env python3

"""Unit tests for Prometheus metrics module."""

import pytest
from prometheus_client import REGISTRY
from metrics import (
    init_metrics,
    record_reconcile_success,
    record_reconcile_error,
    record_ssh_connection,
    record_git_clone,
    record_nixos_build,
    operator_info,
    machines_total,
    reconcile_duration,
    ssh_connections_total,
    git_clones_total,
    nixos_builds_total,
)


class TestMetricsInitialization:
    """Tests for metrics initialization."""

    def test_init_metrics(self):
        """init_metrics should set operator info."""
        init_metrics()
        # Verify operator_info was set (it's a Counter-like metric)
        # We can't easily inspect Info metrics, but we can verify it doesn't crash
        assert operator_info is not None


class TestMetricsRecording:
    """Tests for metric recording helper functions."""

    def test_record_reconcile_success(self):
        """Should record successful reconciliation with duration."""
        namespace = "test-ns"
        configuration = "test-config"
        duration = 1.5

        # Record before value
        before = reconcile_duration.labels(
            namespace=namespace, configuration=configuration
        )._sum.get()

        record_reconcile_success(namespace, configuration, duration)

        # Record after value
        after = reconcile_duration.labels(
            namespace=namespace, configuration=configuration
        )._sum.get()

        # Duration should increase
        assert after > before

    def test_record_reconcile_error(self):
        """Should increment reconciliation error counter."""
        namespace = "test-ns"
        configuration = "test-config"
        error_type = "GitCloneError"

        # Get before value
        metric = REGISTRY.get_sample_value(
            "nio_reconcile_errors_total",
            labels={
                "namespace": namespace,
                "configuration": configuration,
                "error_type": error_type,
            },
        )
        before = metric if metric is not None else 0

        record_reconcile_error(namespace, configuration, error_type)

        # Get after value
        metric = REGISTRY.get_sample_value(
            "nio_reconcile_errors_total",
            labels={
                "namespace": namespace,
                "configuration": configuration,
                "error_type": error_type,
            },
        )
        after = metric if metric is not None else 0

        assert after == before + 1

    def test_record_ssh_connection_success(self):
        """Should record successful SSH connection."""
        namespace = "test-ns"
        machine = "test-machine"
        duration = 0.5

        # Get before value
        metric = REGISTRY.get_sample_value(
            "nio_ssh_connections_total",
            labels={"namespace": namespace, "machine": machine, "result": "success"},
        )
        before = metric if metric is not None else 0

        record_ssh_connection(namespace, machine, success=True, duration=duration)

        # Get after value
        metric = REGISTRY.get_sample_value(
            "nio_ssh_connections_total",
            labels={"namespace": namespace, "machine": machine, "result": "success"},
        )
        after = metric if metric is not None else 0

        assert after == before + 1

    def test_record_ssh_connection_failure(self):
        """Should record failed SSH connection."""
        namespace = "test-ns"
        machine = "test-machine"
        duration = 0.1

        # Get before value
        metric = REGISTRY.get_sample_value(
            "nio_ssh_connections_total",
            labels={"namespace": namespace, "machine": machine, "result": "failure"},
        )
        before = metric if metric is not None else 0

        record_ssh_connection(namespace, machine, success=False, duration=duration)

        # Get after value
        metric = REGISTRY.get_sample_value(
            "nio_ssh_connections_total",
            labels={"namespace": namespace, "machine": machine, "result": "failure"},
        )
        after = metric if metric is not None else 0

        assert after == before + 1

    def test_record_git_clone_success(self):
        """Should record successful git clone."""
        namespace = "test-ns"
        repository = "owner/repo"
        duration = 2.0

        # Get before value
        metric = REGISTRY.get_sample_value(
            "nio_git_clones_total",
            labels={
                "namespace": namespace,
                "repository": repository,
                "result": "success",
            },
        )
        before = metric if metric is not None else 0

        record_git_clone(namespace, repository, success=True, duration=duration)

        # Get after value
        metric = REGISTRY.get_sample_value(
            "nio_git_clones_total",
            labels={
                "namespace": namespace,
                "repository": repository,
                "result": "success",
            },
        )
        after = metric if metric is not None else 0

        assert after == before + 1

    def test_record_git_clone_failure(self):
        """Should record failed git clone."""
        namespace = "test-ns"
        repository = "owner/repo"
        duration = 0.5

        # Get before value
        metric = REGISTRY.get_sample_value(
            "nio_git_clones_total",
            labels={
                "namespace": namespace,
                "repository": repository,
                "result": "failure",
            },
        )
        before = metric if metric is not None else 0

        record_git_clone(namespace, repository, success=False, duration=duration)

        # Get after value
        metric = REGISTRY.get_sample_value(
            "nio_git_clones_total",
            labels={
                "namespace": namespace,
                "repository": repository,
                "result": "failure",
            },
        )
        after = metric if metric is not None else 0

        assert after == before + 1

    def test_record_nixos_build_success(self):
        """Should record successful NixOS build."""
        namespace = "test-ns"
        machine = "test-machine"
        build_type = "switch"
        duration = 300.0

        # Get before value
        metric = REGISTRY.get_sample_value(
            "nio_nixos_builds_total",
            labels={
                "namespace": namespace,
                "machine": machine,
                "build_type": build_type,
                "result": "success",
            },
        )
        before = metric if metric is not None else 0

        record_nixos_build(namespace, machine, build_type, success=True, duration=duration)

        # Get after value
        metric = REGISTRY.get_sample_value(
            "nio_nixos_builds_total",
            labels={
                "namespace": namespace,
                "machine": machine,
                "build_type": build_type,
                "result": "success",
            },
        )
        after = metric if metric is not None else 0

        assert after == before + 1

    def test_record_nixos_build_failure(self):
        """Should record failed NixOS build."""
        namespace = "test-ns"
        machine = "test-machine"
        build_type = "boot"
        duration = 10.0

        # Get before value
        metric = REGISTRY.get_sample_value(
            "nio_nixos_builds_total",
            labels={
                "namespace": namespace,
                "machine": machine,
                "build_type": build_type,
                "result": "failure",
            },
        )
        before = metric if metric is not None else 0

        record_nixos_build(namespace, machine, build_type, success=False, duration=duration)

        # Get after value
        metric = REGISTRY.get_sample_value(
            "nio_nixos_builds_total",
            labels={
                "namespace": namespace,
                "machine": machine,
                "build_type": build_type,
                "result": "failure",
            },
        )
        after = metric if metric is not None else 0

        assert after == before + 1


class TestMetricsAvailability:
    """Tests that all expected metrics are defined."""

    def test_all_metrics_exist(self):
        """All documented metrics should be defined."""
        # Just verify they're importable and have expected types
        assert operator_info is not None
        assert machines_total is not None
        assert reconcile_duration is not None
        assert ssh_connections_total is not None
        assert git_clones_total is not None
        assert nixos_builds_total is not None
