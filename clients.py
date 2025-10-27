#!/usr/bin/env python3

import base64
import kubernetes
import logging
import sys
from typing import Dict
import os
from pathlib import Path
logger = logging.getLogger(__name__)


def setup_kubernetes_client():
    kubeconfig_path = os.environ.get("KUBECONFIG", "~/.kube/config")
    expanded_kubeconfig = os.path.expanduser(kubeconfig_path)
    kubeconfig_file = Path(expanded_kubeconfig)

    logger.info(f"Попытка подключения к Kubernetes")
    logger.info(f"Переменная KUBECONFIG: {kubeconfig_path}")
    logger.info(f"Раскрытый путь к kubeconfig: {expanded_kubeconfig}")

    # Проверяем существование файла
    if kubeconfig_file.exists():
        logger.info(f"Файл kubeconfig найден: {kubeconfig_file}")
        try:
            stat = kubeconfig_file.stat()
            logger.info(f"Права на файл: {oct(stat.st_mode)[-3:]}")
            logger.info(f"Размер файла: {stat.st_size} байт")
        except Exception as e:
            logger.warning(f"Не удалось получить метаданные файла: {e}")

        # Пытаемся загрузить kubeconfig
        try:
            kubernetes.config.load_kube_config(config_file=expanded_kubeconfig)
            logger.info("✅ Успешно загружен kubeconfig")
            return
        except kubernetes.config.ConfigException as e:
            logger.warning(f"❌ Не удалось загрузить kubeconfig: {e}")
        except Exception as e:
            logger.error(f"❗ Неожиданная ошибка при загрузке kubeconfig: {e}", exc_info=True)
    else:
        logger.warning(f"Файл kubeconfig НЕ найден: {kubeconfig_file}")

    # Если kubeconfig не сработал — пробуем in-cluster
    logger.info("Переключаюсь на попытку in-cluster подключения...")

    # Проверяем наличие переменных окружения для in-cluster
    host = os.environ.get("KUBERNETES_SERVICE_HOST")
    port = os.environ.get("KUBERNETES_SERVICE_PORT")
    logger.info(f"KUBERNETES_SERVICE_HOST: {host}")
    logger.info(f"KUBERNETES_SERVICE_PORT: {port}")

    if not host or not port:
        logger.error("❌ Переменные KUBERNETES_SERVICE_HOST и KUBERNETES_SERVICE_PORT не установлены — in-cluster config невозможен")

    try:
        kubernetes.config.load_incluster_config()
        logger.info("✅ Успешно загружен in-cluster config")
    except kubernetes.config.ConfigException as e:
        logger.error(f"❌ Ошибка in-cluster подключения: {e}")
        sys.exit(1)
    except Exception as e:
        logger.error(f"❗ Критическая ошибка при in-cluster подключении: {e}", exc_info=True)
        sys.exit(1)

# Вызов инициализации
setup_kubernetes_client()

# Глобальные клиенты Kubernetes
api_client = kubernetes.client.ApiClient()
core_v1 = kubernetes.client.CoreV1Api()
custom_objects_api = kubernetes.client.CustomObjectsApi()


async def get_secret_data(secret_name: str, namespace: str) -> Dict[str, str]:
    """Получить данные из Kubernetes Secret"""
    try:
        secret = core_v1.read_namespaced_secret(secret_name, namespace)
        if not secret.data:
            return {}
        return {
            key: base64.b64decode(value).decode("utf-8") if value else ""
            for key, value in secret.data.items()
        }
    except Exception as e:
        logger.error(f"Failed to get secret {secret_name}: {e}")
        raise


async def update_machine_status(
    machine_name: str, namespace: str, status_updates: Dict, patch: bool = True
):
    """Обновить статус Machine ресурса"""
    try:
        body = {"status": status_updates}

        if patch:
            custom_objects_api.patch_namespaced_custom_object_status(
                group="nixos.infra",
                version="v1alpha1",
                namespace=namespace,
                plural="machines",
                name=machine_name,
                body=body,
            )
        else:
            # Для создания статуса
            pass

    except Exception as e:
        logger.error(f"Failed to update machine status: {e}")
        raise


async def update_configuration_status(
    config_name: str, namespace: str, status_updates: Dict
):
    """Обновить статус NixosConfiguration ресурса"""
    try:
        body = {"status": status_updates}

        custom_objects_api.patch_namespaced_custom_object_status(
            group="nixos.infra",
            version="v1alpha1",
            namespace=namespace,
            plural="nixosconfigurations",
            name=config_name,
            body=body,
        )

    except Exception as e:
        logger.error(f"Failed to update configuration status: {e}")
        raise


def get_machine(machine_name: str, namespace: str):
    """Получить Machine ресурс"""
    return custom_objects_api.get_namespaced_custom_object(
        group="nixos.infra",
        version="v1alpha1",
        namespace=namespace,
        plural="machines",
        name=machine_name,
    )
