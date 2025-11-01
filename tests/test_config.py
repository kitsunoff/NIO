#!/usr/bin/env python3

"""Unit tests for configuration module."""

import os
import pytest


class TestConfigurationDefaults:
    """Tests for configuration default values."""

    def test_default_values_exist(self):
        """All configuration values should have defaults."""
        # Import fresh to get defaults
        import importlib
        import config as config_module

        importlib.reload(config_module)

        # Check that all expected config values exist
        assert hasattr(config_module, "BASE_CONFIG_PATH")
        assert hasattr(config_module, "KNOWN_HOSTS_PATH")
        assert hasattr(config_module, "REMOTE_HARDWARE_SCRIPT_PATH")
        assert hasattr(config_module, "MACHINE_DISCOVERY_INTERVAL")
        assert hasattr(config_module, "HARDWARE_SCAN_INTERVAL")
        assert hasattr(config_module, "CONFIG_RECONCILE_INTERVAL")
        assert hasattr(config_module, "NIXOS_APPLY_TIMEOUT")
        assert hasattr(config_module, "RETRY_MAX_ATTEMPTS")
        assert hasattr(config_module, "RETRY_INITIAL_DELAY")
        assert hasattr(config_module, "RETRY_MAX_DELAY")
        assert hasattr(config_module, "RETRY_EXPONENTIAL_BASE")
        assert hasattr(config_module, "METRICS_PORT")

    def test_intervals_are_positive(self):
        """Interval values should be positive."""
        import config

        assert config.MACHINE_DISCOVERY_INTERVAL > 0
        assert config.HARDWARE_SCAN_INTERVAL > 0
        assert config.CONFIG_RECONCILE_INTERVAL > 0

    def test_timeout_is_positive(self):
        """Timeout value should be positive."""
        import config

        assert config.NIXOS_APPLY_TIMEOUT > 0

    def test_retry_config_is_valid(self):
        """Retry configuration should be valid."""
        import config

        assert config.RETRY_MAX_ATTEMPTS >= 1
        assert config.RETRY_INITIAL_DELAY > 0
        assert config.RETRY_MAX_DELAY >= config.RETRY_INITIAL_DELAY
        assert config.RETRY_EXPONENTIAL_BASE > 1.0

    def test_metrics_port_is_valid(self):
        """Metrics port should be valid."""
        import config

        assert 1 <= config.METRICS_PORT <= 65535


class TestConfigurationEnvironmentOverride:
    """Tests for environment variable overrides."""

    def test_env_override_string(self):
        """String config can be overridden by environment."""
        os.environ["NIO_BASE_CONFIG_PATH"] = "/custom/path"

        import importlib
        import config as config_module

        importlib.reload(config_module)

        assert config_module.BASE_CONFIG_PATH == "/custom/path"

        # Cleanup
        del os.environ["NIO_BASE_CONFIG_PATH"]

    def test_env_override_int(self):
        """Integer config can be overridden by environment."""
        os.environ["NIO_RETRY_MAX_ATTEMPTS"] = "10"

        import importlib
        import config as config_module

        importlib.reload(config_module)

        assert config_module.RETRY_MAX_ATTEMPTS == 10

        # Cleanup
        del os.environ["NIO_RETRY_MAX_ATTEMPTS"]

    def test_env_override_float(self):
        """Float config can be overridden by environment."""
        os.environ["NIO_RETRY_INITIAL_DELAY"] = "5.5"

        import importlib
        import config as config_module

        importlib.reload(config_module)

        assert config_module.RETRY_INITIAL_DELAY == 5.5

        # Cleanup
        del os.environ["NIO_RETRY_INITIAL_DELAY"]


class TestConfigSummary:
    """Tests for configuration summary."""

    def test_config_summary_format(self):
        """Config summary should be properly formatted."""
        import config

        summary = config.get_config_summary()

        # Should be a string
        assert isinstance(summary, str)

        # Should contain key configuration items
        assert "Base config path" in summary
        assert "Machine discovery" in summary
        assert "Retry" in summary
        assert "Metrics" in summary
