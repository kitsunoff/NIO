#!/usr/bin/env python3

import kopf
import logging
import os

from machine_handlers import check_machine_discoverable, scan_machine_hardware
from nixosconfiguration_handlers import reconcile_nixos_configuration
from clients import update_machine_status, get_machine


# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)
logger.info("NixOS Infrastructure Operator starting")

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


@kopf.timer("nio.homystack.com", "v1alpha1", "machines", interval=60.0)
async def check_machine_discoverability(body, spec, name, namespace, **kwargs):
    """Periodic machine availability check"""
    logger.debug(f"Checking discoverability for machine: {name}")

    # Check machine availability with body for events
    is_discoverable = await check_machine_discoverable(spec, body, name, namespace)

    # Update status
    await update_machine_status(name, namespace, {"discoverable": is_discoverable})


@kopf.timer("nio.homystack.com", "v1alpha1", "machines", interval=300.0)  # Every 5 minutes
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
@kopf.on.timer("nio.homystack.com", "v1alpha1", "nixosconfigurations", interval=120)
async def unified_nixos_configuration_handler(body, spec, name, namespace, **kwargs):
    """Unified handler for all NixosConfiguration operations"""
    await reconcile_nixos_configuration(body, spec, name, namespace, **kwargs)


@kopf.on.startup()
def configure(settings: kopf.OperatorSettings, **_):
    settings.posting.level = logging.WARNING


if __name__ == "__main__":
    kopf.run()
