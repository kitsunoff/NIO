#!/usr/bin/env python3

import kopf
import logging

from machine_handlers import check_machine_discoverable, scan_machine_hardware
from nixosconfiguration_handlers import reconcile_nixos_configuration
from clients import update_machine_status, get_machine
import os


# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)
print("starting")

# --- Add Nix path to PATH ---
nix_bin_path = "/nix/var/nix/profiles/default/bin"
current_path = os.environ.get("PATH", "")
if nix_bin_path not in current_path:
    os.environ["PATH"] = f"{nix_bin_path}:{current_path}"
    logger.info(f"Added Nix path to PATH: {nix_bin_path}")


# Machine handlers
@kopf.on.create("nixos.infra", "v1alpha1", "machines")
async def on_machine_create(body, spec, name, namespace, **kwargs):
    """Обработчик создания Machine"""
    logger.info(f"Creating Machine: {name}")

    # Проверка доступности машины с передачей body для событий
    is_discoverable = await check_machine_discoverable(spec, body, name, namespace)

    # Установка начального статуса
    await update_machine_status(
        name, namespace, {"discoverable": is_discoverable, "hasConfiguration": False}
    )


@kopf.timer("nixos.infra", "v1alpha1", "machines", interval=60.0)
async def check_machine_discoverability(body, spec, name, namespace, **kwargs):
    """Периодическая проверка доступности машин"""
    logger.debug(f"Checking discoverability for machine: {name}")

    # Проверка доступности машины с передачей body для событий
    is_discoverable = await check_machine_discoverable(spec, body, name, namespace)

    # Обновление статуса
    await update_machine_status(name, namespace, {"discoverable": is_discoverable})


@kopf.timer("nixos.infra", "v1alpha1", "machines", interval=300.0)  # Каждые 5 минут
async def scan_machine_hardware_periodically(body, spec, name, namespace, **kwargs):
    """Периодическое сканирование железа машин"""
    logger.debug(f"Scanning hardware for machine: {name}")

    # Проверяем доступность машины перед сканированием
    is_discoverable = await check_machine_discoverable(spec, body, name, namespace)

    if not is_discoverable:
        logger.warning(f"Machine {name} is not discoverable, skipping hardware scan")
        return

    # Сканируем железо
    hardware_facts = await scan_machine_hardware(spec, body, name, namespace)

    # Обновляем статус с фактами о железе
    from datetime import datetime

    await update_machine_status(
        name,
        namespace,
        {
            "hardwareFacts": hardware_facts,
        },
    )


# NixOSConfiguration handlers
@kopf.on.create("nixos.infra", "v1alpha1", "nixosconfigurations")
@kopf.on.update("nixos.infra", "v1alpha1", "nixosconfigurations")
@kopf.on.resume("nixos.infra", "v1alpha1", "nixosconfigurations")
@kopf.on.delete("nixos.infra", "v1alpha1", "nixosconfigurations")
async def unified_nixos_configuration_handler(body, spec, name, namespace, **kwargs):
    """Унифицированный обработчик для всех операций с NixosConfiguration"""
    await reconcile_nixos_configuration(body, spec, name, namespace, **kwargs)


@kopf.on.startup()
def configure(settings: kopf.OperatorSettings, **_):
    settings.posting.level = logging.WARNING


if __name__ == "__main__":
    kopf.run()
