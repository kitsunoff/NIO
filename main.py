#!/usr/bin/env python3

import kopf
import logging
import os
import signal
import asyncio

from machine_handlers import check_machine_discoverable, scan_machine_hardware
from nixosconfiguration_handlers import reconcile_nixos_configuration
from clients import update_machine_status, get_machine
from metrics import init_metrics
from prometheus_client import start_http_server
from health import run_health_server, HealthCheckServer
import config


# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)
logger.info("NixOS Infrastructure Operator starting")

# Global flag for graceful shutdown
_shutdown_event = asyncio.Event()

# Global health check server instance
_health_server: HealthCheckServer = None

# --- Add Nix path to PATH ---
nix_bin_path = "/nix/var/nix/profiles/default/bin"
current_path = os.environ.get("PATH", "")
if nix_bin_path not in current_path:
    os.environ["PATH"] = f"{nix_bin_path}:{current_path}"
    logger.info(f"Added Nix path to PATH: {nix_bin_path}")


# Machine handlers
@kopf.on.create("nio.homystack.com", "v1alpha1", "machines")
async def on_machine_create(body, spec, name, namespace, **kwargs):
    """Handler for Machine resource creation"""
    logger.info(f"Creating Machine: {name}")

    # Check machine availability with body for events
    is_discoverable = await check_machine_discoverable(spec, body, name, namespace)

    # Set initial status
    await update_machine_status(
        name, namespace, {"discoverable": is_discoverable, "hasConfiguration": False}
    )


@kopf.timer("nio.homystack.com", "v1alpha1", "machines", interval=config.MACHINE_DISCOVERY_INTERVAL)
async def check_machine_discoverability(body, spec, name, namespace, **kwargs):
    """Periodic machine availability check"""
    logger.debug(f"Checking discoverability for machine: {name}")

    # Check machine availability with body for events
    is_discoverable = await check_machine_discoverable(spec, body, name, namespace)

    # Update status
    await update_machine_status(name, namespace, {"discoverable": is_discoverable})


@kopf.timer("nio.homystack.com", "v1alpha1", "machines", interval=config.HARDWARE_SCAN_INTERVAL)
async def scan_machine_hardware_periodically(body, spec, name, namespace, **kwargs):
    """Periodic hardware scanning for machines"""
    logger.debug(f"Scanning hardware for machine: {name}")

    # Check machine availability before scanning
    is_discoverable = await check_machine_discoverable(spec, body, name, namespace)

    if not is_discoverable:
        logger.warning(f"Machine {name} is not discoverable, skipping hardware scan")
        return

    # Scan hardware
    hardware_facts = await scan_machine_hardware(spec, body, name, namespace)

    # Update status with hardware facts
    await update_machine_status(
        name,
        namespace,
        {
            "hardwareFacts": hardware_facts,
        },
    )


# NixOSConfiguration handlers
@kopf.on.create("nio.homystack.com", "v1alpha1", "nixosconfigurations")
@kopf.on.update("nio.homystack.com", "v1alpha1", "nixosconfigurations")
@kopf.on.resume("nio.homystack.com", "v1alpha1", "nixosconfigurations")
@kopf.on.delete("nio.homystack.com", "v1alpha1", "nixosconfigurations")
@kopf.on.timer("nio.homystack.com", "v1alpha1", "nixosconfigurations", interval=config.CONFIG_RECONCILE_INTERVAL)
async def unified_nixos_configuration_handler(body, spec, name, namespace, **kwargs):
    """Unified handler for all NixosConfiguration operations"""
    await reconcile_nixos_configuration(body, spec, name, namespace, **kwargs)


@kopf.on.startup()
async def configure(settings: kopf.OperatorSettings, **_):
    global _health_server

    settings.posting.level = logging.WARNING

    # Initialize Prometheus metrics
    init_metrics()

    # Start Prometheus metrics server
    start_http_server(config.METRICS_PORT)
    logger.info(f"Prometheus metrics server started on port {config.METRICS_PORT}")

    # Start health check server
    _health_server = await run_health_server(
        host="0.0.0.0",
        port=config.HEALTH_CHECK_PORT
    )
    # Mark as ready after initialization
    _health_server.mark_ready()
    logger.info(f"Health check server started on port {config.HEALTH_CHECK_PORT}")

    # Log configuration summary
    logger.info(config.get_config_summary())


def handle_shutdown_signal(signum, frame):
    """Handle shutdown signals for graceful termination"""
    signal_name = signal.Signals(signum).name
    logger.info(f"Received {signal_name} signal, initiating graceful shutdown...")
    _shutdown_event.set()


@kopf.on.cleanup()
async def cleanup_handler(**kwargs):
    """Cleanup handler called on operator shutdown"""
    global _health_server

    logger.info("Operator cleanup: draining active reconciliations...")

    # Mark as not ready to stop receiving traffic
    if _health_server:
        _health_server.mark_not_ready()

    # Give active reconciliations time to complete
    await asyncio.sleep(5)

    # Stop health check server
    if _health_server:
        await _health_server.stop()

    logger.info("Operator cleanup complete")


if __name__ == "__main__":
    # Register signal handlers for graceful shutdown
    signal.signal(signal.SIGTERM, handle_shutdown_signal)
    signal.signal(signal.SIGINT, handle_shutdown_signal)

    logger.info("Signal handlers registered (SIGTERM, SIGINT)")

    try:
        kopf.run()
    except KeyboardInterrupt:
        logger.info("Operator stopped by user")
    except Exception as e:
        logger.error(f"Operator crashed: {e}", exc_info=True)
        raise
    finally:
        logger.info("NixOS Infrastructure Operator shutdown complete")
