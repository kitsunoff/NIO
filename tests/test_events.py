#!/usr/bin/env python3

"""Unit tests for Kubernetes event emission module."""

import pytest
from unittest.mock import patch, MagicMock
from events import (
    emit_missing_credentials_event,
    emit_configuration_applied_event,
    emit_error_event,
)


class TestEventEmission:
    """Tests for event emission functions."""

    def test_emit_missing_credentials_event_success(self):
        """Should emit warning event for missing credentials."""
        body = {"metadata": {"name": "test-machine"}}
        reason = "MissingCredentials"
        message = "SSH credentials not found"

        with patch("events.kopf.warn") as mock_warn, patch(
            "events.logger"
        ) as mock_logger:
            emit_missing_credentials_event(body, reason, message)

            mock_warn.assert_called_once_with(
                body, reason=reason, message=message
            )
            mock_logger.warning.assert_called_once()

    def test_emit_missing_credentials_event_failure(self):
        """Should handle kopf.warn failure gracefully."""
        body = {"metadata": {"name": "test-machine"}}
        reason = "MissingCredentials"
        message = "SSH credentials not found"

        with patch("events.kopf.warn", side_effect=Exception("Kopf error")), patch(
            "events.logger"
        ) as mock_logger:
            emit_missing_credentials_event(body, reason, message)

            mock_logger.error.assert_called_once()
            assert "Failed to emit missing credentials event" in str(
                mock_logger.error.call_args
            )

    def test_emit_configuration_applied_event_success(self):
        """Should emit info event for configuration applied."""
        body = {"metadata": {"name": "test-config"}}
        reason = "ConfigurationApplied"
        message = "NixOS configuration successfully applied"

        with patch("events.kopf.info") as mock_info, patch(
            "events.logger"
        ) as mock_logger:
            emit_configuration_applied_event(body, reason, message)

            mock_info.assert_called_once_with(
                body, reason=reason, message=message
            )
            mock_logger.info.assert_called_once()

    def test_emit_configuration_applied_event_failure(self):
        """Should handle kopf.info failure gracefully."""
        body = {"metadata": {"name": "test-config"}}
        reason = "ConfigurationApplied"
        message = "NixOS configuration successfully applied"

        with patch("events.kopf.info", side_effect=Exception("Kopf error")), patch(
            "events.logger"
        ) as mock_logger:
            emit_configuration_applied_event(body, reason, message)

            mock_logger.error.assert_called_once()
            assert "Failed to emit configuration applied event" in str(
                mock_logger.error.call_args
            )

    def test_emit_error_event_success(self):
        """Should emit exception event for errors."""
        body = {"metadata": {"name": "test-machine"}}
        reason = "BuildFailed"
        message = "NixOS build failed: syntax error"

        with patch("events.kopf.exception") as mock_exception, patch(
            "events.logger"
        ) as mock_logger:
            emit_error_event(body, reason, message)

            mock_exception.assert_called_once_with(
                body, reason=reason, message=message
            )
            mock_logger.error.assert_called_once()

    def test_emit_error_event_failure(self):
        """Should handle kopf.exception failure gracefully."""
        body = {"metadata": {"name": "test-machine"}}
        reason = "BuildFailed"
        message = "NixOS build failed: syntax error"

        with patch("events.kopf.exception", side_effect=Exception("Kopf error")), patch(
            "events.logger"
        ) as mock_logger:
            emit_error_event(body, reason, message)

            # Should log the failed emission
            mock_logger.error.assert_called_once()
            assert "Failed to emit error event" in str(mock_logger.error.call_args)


class TestEventMessageFormats:
    """Tests for event message formatting and logging."""

    def test_missing_credentials_message_logged(self):
        """Missing credentials event message should be logged."""
        body = {"metadata": {"name": "machine1"}}
        message = "Custom credentials message"

        with patch("events.kopf.warn"), patch("events.logger") as mock_logger:
            emit_missing_credentials_event(body, "Reason", message)

            call_args = str(mock_logger.warning.call_args)
            assert "Emitted missing credentials event" in call_args
            assert message in call_args

    def test_configuration_applied_message_logged(self):
        """Configuration applied event message should be logged."""
        body = {"metadata": {"name": "config1"}}
        message = "Custom config message"

        with patch("events.kopf.info"), patch("events.logger") as mock_logger:
            emit_configuration_applied_event(body, "Reason", message)

            call_args = str(mock_logger.info.call_args)
            assert "Emitted configuration applied event" in call_args
            assert message in call_args

    def test_error_event_message_logged(self):
        """Error event message should be logged."""
        body = {"metadata": {"name": "machine1"}}
        message = "Custom error message"

        with patch("events.kopf.exception"), patch("events.logger") as mock_logger:
            emit_error_event(body, "Reason", message)

            call_args = str(mock_logger.error.call_args)
            assert "Emitted error event" in call_args
            assert message in call_args
