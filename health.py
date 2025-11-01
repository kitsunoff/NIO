"""
Health check endpoints for Kubernetes liveness and readiness probes.

This module provides HTTP endpoints for monitoring the operator's health status:
- /health: General health check (always returns 200 if service is running)
- /ready: Readiness probe (checks if operator is ready to handle requests)
- /live: Liveness probe (checks if operator is alive and not deadlocked)
"""

import asyncio
import logging
from typing import Optional

from aiohttp import web

logger = logging.getLogger(__name__)


class HealthCheckServer:
    """HTTP server for health check endpoints."""

    def __init__(self, host: str = "0.0.0.0", port: int = 8080):
        """
        Initialize health check server.

        Args:
            host: Host to bind to (default: 0.0.0.0 for all interfaces)
            port: Port to listen on (default: 8080)
        """
        self.host = host
        self.port = port
        self.app = web.Application()
        self._setup_routes()
        self.runner: Optional[web.AppRunner] = None
        self._is_ready = False

    def _setup_routes(self):
        """Configure HTTP routes for health endpoints."""
        self.app.router.add_get("/health", self.health_handler)
        self.app.router.add_get("/ready", self.readiness_handler)
        self.app.router.add_get("/live", self.liveness_handler)

    async def health_handler(self, request: web.Request) -> web.Response:
        """
        General health check endpoint.

        Returns 200 OK if the service is running.
        Used for general health monitoring.
        """
        return web.json_response({"status": "healthy"})

    async def readiness_handler(self, request: web.Request) -> web.Response:
        """
        Readiness probe endpoint.

        Returns 200 OK if the operator is ready to handle requests.
        Returns 503 Service Unavailable if not ready (e.g., during startup).

        Kubernetes uses this to determine when to send traffic to the pod.
        """
        if self._is_ready:
            return web.json_response({"status": "ready"})
        return web.json_response(
            {"status": "not ready", "reason": "operator initializing"},
            status=503,
        )

    async def liveness_handler(self, request: web.Request) -> web.Response:
        """
        Liveness probe endpoint.

        Returns 200 OK if the operator is alive and functional.
        If this fails, Kubernetes will restart the pod.

        Currently always returns healthy - can be extended to detect deadlocks.
        """
        return web.json_response({"status": "alive"})

    def mark_ready(self):
        """Mark the operator as ready to handle requests."""
        self._is_ready = True
        logger.info("Operator marked as ready")

    def mark_not_ready(self):
        """Mark the operator as not ready (e.g., during shutdown)."""
        self._is_ready = False
        logger.info("Operator marked as not ready")

    async def start(self):
        """Start the health check HTTP server."""
        self.runner = web.AppRunner(self.app)
        await self.runner.setup()
        site = web.TCPSite(self.runner, self.host, self.port)
        await site.start()
        logger.info(f"Health check server started on {self.host}:{self.port}")

    async def stop(self):
        """Stop the health check HTTP server gracefully."""
        if self.runner:
            await self.runner.cleanup()
            logger.info("Health check server stopped")


async def run_health_server(host: str = "0.0.0.0", port: int = 8080) -> HealthCheckServer:
    """
    Create and start a health check server.

    Args:
        host: Host to bind to
        port: Port to listen on

    Returns:
        Running HealthCheckServer instance
    """
    server = HealthCheckServer(host, port)
    await server.start()
    return server
