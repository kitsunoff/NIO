#!/usr/bin/env python3

"""Unit tests for health check server module."""

import pytest
from unittest.mock import Mock
from aiohttp import web
from health import HealthCheckServer, run_health_server


@pytest.mark.asyncio
class TestHealthCheckServer:
    """Tests for HealthCheckServer class."""

    async def test_initialization(self):
        """Server should initialize with default host and port."""
        server = HealthCheckServer()
        assert server.host == "0.0.0.0"
        assert server.port == 8080
        assert server._is_ready is False
        assert server.runner is None

    async def test_initialization_custom_port(self):
        """Server should accept custom host and port."""
        server = HealthCheckServer(host="127.0.0.1", port=9090)
        assert server.host == "127.0.0.1"
        assert server.port == 9090

    async def test_health_handler(self):
        """Health handler should return 200 with healthy status."""
        server = HealthCheckServer()
        request = Mock(spec=web.Request)
        response = await server.health_handler(request)
        assert response.status == 200
        assert response.content_type == "application/json"

    async def test_readiness_handler_not_ready(self):
        """Readiness handler should return 503 when not ready."""
        server = HealthCheckServer()
        request = Mock(spec=web.Request)
        response = await server.readiness_handler(request)
        assert response.status == 503

    async def test_readiness_handler_ready(self):
        """Readiness handler should return 200 when ready."""
        server = HealthCheckServer()
        server.mark_ready()
        request = Mock(spec=web.Request)
        response = await server.readiness_handler(request)
        assert response.status == 200

    async def test_liveness_handler(self):
        """Liveness handler should always return 200."""
        server = HealthCheckServer()
        request = Mock(spec=web.Request)
        response = await server.liveness_handler(request)
        assert response.status == 200

    async def test_mark_ready(self):
        """mark_ready should set _is_ready to True."""
        server = HealthCheckServer()
        assert server._is_ready is False
        server.mark_ready()
        assert server._is_ready is True

    async def test_mark_not_ready(self):
        """mark_not_ready should set _is_ready to False."""
        server = HealthCheckServer()
        server.mark_ready()
        assert server._is_ready is True
        server.mark_not_ready()
        assert server._is_ready is False

    async def test_start_and_stop(self):
        """Server should start and stop gracefully."""
        server = HealthCheckServer(host="127.0.0.1", port=18080)
        try:
            # Start server
            await server.start()
            assert server.runner is not None

            # Stop server
            await server.stop()
        except OSError as e:
            # Port might be in use, that's ok for this test
            pytest.skip(f"Port binding failed: {e}")

    async def test_run_health_server(self):
        """run_health_server should create and start server."""
        try:
            server = await run_health_server(host="127.0.0.1", port=18081)
            assert isinstance(server, HealthCheckServer)
            assert server.runner is not None
            await server.stop()
        except OSError as e:
            pytest.skip(f"Port binding failed: {e}")


@pytest.mark.asyncio
class TestHealthCheckEndpoints:
    """Integration tests for health check endpoints."""

    async def test_routes_configured(self):
        """Server should have all routes configured."""
        server = HealthCheckServer()
        # Get all resources and extract paths
        paths = []
        for resource in server.app.router.resources():
            paths.append(resource.canonical)
        assert "/health" in paths
        assert "/ready" in paths
        assert "/live" in paths

    async def test_ready_state_transitions(self):
        """Server should handle ready state transitions correctly."""
        server = HealthCheckServer()

        # Initially not ready
        assert server._is_ready is False

        # Mark ready
        server.mark_ready()
        assert server._is_ready is True

        # Mark not ready
        server.mark_not_ready()
        assert server._is_ready is False

        # Mark ready again
        server.mark_ready()
        assert server._is_ready is True
