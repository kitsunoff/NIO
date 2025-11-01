#!/usr/bin/env python3

import base64
import kubernetes
import logging
import sys
import os
from typing import Dict, Any
from pathlib import Path

logger = logging.getLogger(__name__)


def setup_kubernetes_client() -> None:
    kubeconfig_path = os.environ.get("KUBECONFIG", "~/.kube/config")
    expanded_kubeconfig = os.path.expanduser(kubeconfig_path)
    kubeconfig_file = Path(expanded_kubeconfig)

    logger.info(f"Attempting to connect to Kubernetes")
    logger.info(f"KUBECONFIG variable: {kubeconfig_path}")
    logger.info(f"Expanded kubeconfig path: {expanded_kubeconfig}")

    # Check if file exists
    if kubeconfig_file.exists():
        logger.info(f"Kubeconfig file found: {kubeconfig_file}")
        try:
            stat = kubeconfig_file.stat()
            logger.info(f"File permissions: {oct(stat.st_mode)[-3:]}")
            logger.info(f"File size: {stat.st_size} bytes")
        except Exception as e:
            logger.warning(f"Failed to get file metadata: {e}")

        # Try to load kubeconfig
        try:
            kubernetes.config.load_kube_config(config_file=expanded_kubeconfig)
            logger.info("Successfully loaded kubeconfig")
            return
        except kubernetes.config.ConfigException as e:
            logger.warning(f"Failed to load kubeconfig: {e}")
        except Exception as e:
            logger.error(f"Unexpected error loading kubeconfig: {e}", exc_info=True)
    else:
        logger.warning(f"Kubeconfig file NOT found: {kubeconfig_file}")

    # If kubeconfig doesn't work - try in-cluster
    logger.info("Switching to in-cluster connection attempt...")

    # Check for in-cluster environment variables
    host = os.environ.get("KUBERNETES_SERVICE_HOST")
    port = os.environ.get("KUBERNETES_SERVICE_PORT")
    logger.info(f"KUBERNETES_SERVICE_HOST: {host}")
    logger.info(f"KUBERNETES_SERVICE_PORT: {port}")

    if not host or not port:
        logger.error("KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT variables not set - in-cluster config impossible")

    try:
        kubernetes.config.load_incluster_config()
        logger.info("Successfully loaded in-cluster config")
    except kubernetes.config.ConfigException as e:
        logger.error(f"In-cluster connection error: {e}")
        sys.exit(1)
    except Exception as e:
        logger.error(f"Critical error during in-cluster connection: {e}", exc_info=True)
        sys.exit(1)

# Call initialization
setup_kubernetes_client()

# Global Kubernetes clients
api_client = kubernetes.client.ApiClient()
core_v1 = kubernetes.client.CoreV1Api()
custom_objects_api = kubernetes.client.CustomObjectsApi()


async def get_secret_data(secret_name: str, namespace: str) -> Dict[str, str]:
    """Get data from Kubernetes Secret"""
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
    machine_name: str, namespace: str, status_updates: Dict[str, Any], patch: bool = True
) -> None:
    """Update Machine resource status"""
    try:
        body = {"status": status_updates}

        if patch:
            custom_objects_api.patch_namespaced_custom_object_status(
                group="nio.homystack.com",
                version="v1alpha1",
                namespace=namespace,
                plural="machines",
                name=machine_name,
                body=body,
            )

    except Exception as e:
        logger.error(f"Failed to update machine status: {e}", exc_info=True)
        raise


async def update_configuration_status(
    config_name: str, namespace: str, status_updates: Dict[str, Any]
) -> None:
    """Update NixosConfiguration resource status"""
    try:
        body = {"status": status_updates}

        custom_objects_api.patch_namespaced_custom_object_status(
            group="nio.homystack.com",
            version="v1alpha1",
            namespace=namespace,
            plural="nixosconfigurations",
            name=config_name,
            body=body,
        )

    except Exception as e:
        logger.error(f"Failed to update configuration status: {e}", exc_info=True)
        raise


def get_machine(machine_name: str, namespace: str) -> Dict[str, Any]:
    """Get Machine resource"""
    return custom_objects_api.get_namespaced_custom_object(
        group="nio.homystack.com",
        version="v1alpha1",
        namespace=namespace,
        plural="machines",
        name=machine_name,
    )
