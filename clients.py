#!/usr/bin/env python3

import base64
import kubernetes
import logging
import sys
from typing import Dict

logger = logging.getLogger(__name__)


# Подключение к Kubernetes
try:
    kubernetes.config.load_kube_config()
    logger.info("Kubernetes config loaded from kubeconfig file.")
except kubernetes.config.ConfigException:
    try:
        kubernetes.config.load_incluster_config()
        logger.info("Kubernetes config loaded from in-cluster environment.")
    except kubernetes.config.ConfigException as e:
        logger.error(f"Ошибка подключения к K8s: {e}")
        sys.exit(1)

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
            key: base64.b64decode(value).decode('utf-8') if value else ""
            for key, value in secret.data.items()
        }
    except Exception as e:
        logger.error(f"Failed to get secret {secret_name}: {e}")
        raise

async def update_machine_status(machine_name: str, namespace: str, 
                              status_updates: Dict, patch: bool = True):
    """Обновить статус Machine ресурса"""
    try:
        body = {
            "status": status_updates
        }
        
        if patch:
            custom_objects_api.patch_namespaced_custom_object_status(
                group="nixos.infra",
                version="v1alpha1",
                namespace=namespace,
                plural="machines",
                name=machine_name,
                body=body
            )
        else:
            # Для создания статуса
            pass
            
    except Exception as e:
        logger.error(f"Failed to update machine status: {e}")
        raise

async def update_configuration_status(config_name: str, namespace: str,
                                    status_updates: Dict):
    """Обновить статус NixosConfiguration ресурса"""
    try:
        body = {
            "status": status_updates
        }
        
        custom_objects_api.patch_namespaced_custom_object_status(
            group="nixos.infra",
            version="v1alpha1",
            namespace=namespace,
            plural="nixosconfigurations",
            name=config_name,
            body=body
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
        name=machine_name
    )
