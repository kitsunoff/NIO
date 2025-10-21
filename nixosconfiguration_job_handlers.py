#!/usr/bin/env python3

import logging
import shutil
import asyncio
import os
import tempfile
import tarfile
import io
import pathlib
from datetime import datetime
import kubernetes
from kubernetes.stream import stream
from typing import Dict, Optional

from machine_handlers import check_machine_discoverable
from clients import get_machine, update_machine_status, update_configuration_status, get_secret_data
from utils import clone_git_repo

logger = logging.getLogger(__name__)

# Инициализация клиентов Kubernetes
kubernetes.config.load_kube_config()
batch_v1 = kubernetes.client.BatchV1Api()
core_v1 = kubernetes.client.CoreV1Api()

async def prepare_configuration_tarball(
    config_spec: Dict, 
    namespace: str
) -> str:
    """Подготовить tarball с конфигурацией и дополнительными файлами"""
    temp_dir = tempfile.mkdtemp(prefix="nixos-config-")
    
    try:
        # Клонирование Git репозитория
        repo_path, commit_hash = await clone_git_repo(
            config_spec['gitRepo'],
            config_spec.get('credentialsRef'),
            namespace
        )
        
        # Копируем репозиторий в временную директорию
        config_dir = os.path.join(temp_dir, "config")
        shutil.copytree(repo_path, config_dir)
        
        # Подкладываем дополнительные файлы
        await prepare_additional_files(config_spec, namespace, config_dir)
        
        # Создаем tarball
        tarball_path = os.path.join(temp_dir, "configuration.tar.gz")
        with tarfile.open(tarball_path, "w:gz") as tar:
            tar.add(config_dir, arcname=".")
        
        logger.info(f"Created configuration tarball at {tarball_path}")
        return tarball_path, commit_hash
        
    except Exception as e:
        shutil.rmtree(temp_dir, ignore_errors=True)
        raise e

async def prepare_additional_files(config_spec: Dict, namespace: str, repo_path: str) -> None:
    """Подготовить дополнительные файлы в репозитории"""
    
    if 'additionalFiles' in config_spec:
        for file_spec in config_spec['additionalFiles']:
            file_path = os.path.join(repo_path, file_spec['path'])
            os.makedirs(os.path.dirname(file_path), exist_ok=True)
            
            if file_spec['valueType'] == 'Inline':
                with open(file_path, 'w') as f:
                    f.write(file_spec['inline'])
                logger.info(f"Created inline file: {file_spec['path']}")
                
            elif file_spec['valueType'] == 'SecretRef':
                try:
                    secret_data = await get_secret_data(file_spec['secretRef']['name'], namespace)
                    # Записываем все ключи секрета как отдельные файлы
                    for key, value in secret_data.items():
                        secret_file_path = os.path.join(repo_path, file_spec['path'], key)
                        os.makedirs(os.path.dirname(secret_file_path), exist_ok=True)
                        with open(secret_file_path, 'w') as f:
                            f.write(value)
                        logger.info(f"Created secret file: {file_spec['path']}/{key}")
                except Exception as e:
                    logger.error(f"Failed to process secret {file_spec['secretRef']['name']}: {e}")
                    raise
                    
            elif file_spec['valueType'] == 'NixosFacter':
                # Для NixosFacter файлы создаются автоматически при сборке
                logger.info(f"NixosFacter file will be handled by flake: {file_spec['path']}")

def copy_file_to_pod(namespace: str, pod_name: str, source_file: pathlib.Path, dest_path: str):
    """Скопировать файл в Pod через kubernetes.stream"""
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode='w:gz') as tar:
        tar.add(source_file, arcname=pathlib.Path(dest_path).joinpath(source_file.name))
    buf.seek(0)

    # Copying file
    exec_command = ['tar', 'xzvf', '-', '-C', '/']
    resp = stream.stream(core_v1.connect_get_namespaced_pod_exec, pod_name, namespace,
                         command=exec_command,
                         stderr=True, stdin=True,
                         stdout=True, tty=False,
                         _preload_content=False)

    chunk_size = 10 * 1024 * 1024
    while resp.is_open():
        resp.update(timeout=1)
        if resp.peek_stdout():
            logger.debug(f"STDOUT: {resp.read_stdout()}")
        if resp.peek_stderr():
            logger.debug(f"STDERR: {resp.read_stderr()}")
        if read := buf.read(chunk_size):
            resp.write_stdin(read)
        else:
            break
    resp.close()

async def create_nixos_job(
    job_name: str,
    namespace: str,
    machine_spec: Dict,
    config_spec: Dict,
    is_remove: bool = False
) -> bool:
    """Создать Job для применения конфигурации NixOS"""
    try:
        # Определение пути к конфигурации с учетом поддиректории
        config_subdir = config_spec.get('configurationSubdir', '')
        config_base_path = f"/config/{config_subdir}" if config_subdir else "/config"
        
        # Определяем команду в зависимости от режима
        if config_spec.get('fullInstall', False):
            # Полная установка с nixos-anywhere
            flake = config_spec['onRemoveFlake'] if is_remove else config_spec['flake']
            cmd = f"nixos-anywhere --target-host {machine_spec['ipAddress']} --kexec {config_base_path}/{flake}"
        else:
            # Обновление существующей системы с nixos-rebuild
            flake = config_spec['onRemoveFlake'] if is_remove else config_spec['flake']
            cmd = f"nixos-rebuild switch --flake {config_base_path}/{flake} --target-host {machine_spec['ipAddress']}"
        
        # Создаем Job
        job = {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "metadata": {
                "name": job_name,
                "namespace": namespace,
                "labels": {
                    "app": "nixos-configuration",
                    "configuration": config_spec.get('name', 'unknown'),
                    "machine": machine_spec.get('hostname', 'unknown')
                }
            },
            "spec": {
                "template": {
                    "spec": {
                        "containers": [{
                            "name": "nixos-configurator",
                            "image": "nixos/nix:latest",
                            "command": ["/bin/sh", "-c"],
                            "args": [
                                f"""
                                set -e
                                # Ждем появления tar.gz файла
                                echo "Waiting for configuration tarball..."
                                while [ ! -f /tmp/configuration.tar.gz ]; do
                                    sleep 5
                                done
                                
                                # Распаковываем конфигурацию
                                mkdir -p /config
                                cd /config
                                tar -xzf /tmp/configuration.tar.gz
                                rm /tmp/configuration.tar.gz
                                
                                # Применяем конфигурацию
                                {cmd}
                                """
                            ],
                            "env": [
                                {
                                    "name": "NIX_CONFIG",
                                    "value": "experimental-features = nix-command flakes"
                                }
                            ],
                            "resources": {
                                "requests": {
                                    "cpu": "500m",
                                    "memory": "512Mi"
                                },
                                "limits": {
                                    "cpu": "1000m",
                                    "memory": "1Gi"
                                }
                            }
                        }],
                        "restartPolicy": "Never"
                    }
                },
                "backoffLimit": 1,
                "ttlSecondsAfterFinished": 3600  # Удалить Job через час после завершения
            }
        }
        
        # Создаем Job
        batch_v1.create_namespaced_job(namespace, job)
        logger.info(f"Created Job {job_name} for configuration application")
        
        return True
        
    except Exception as e:
        logger.error(f"Failed to create Job {job_name}: {e}")
        return False

async def copy_tarball_to_job(job_name: str, namespace: str, tarball_path: str) -> bool:
    """Скопировать tarball в Pod Job через kubernetes.stream"""
    try:
        # Получаем Pod для Job
        pods = core_v1.list_namespaced_pod(
            namespace=namespace,
            label_selector=f"job-name={job_name}"
        )
        
        if not pods.items:
            logger.error(f"No pods found for Job {job_name}")
            return False
        
        pod_name = pods.items[0].metadata.name
        
        # Ждем пока Pod будет готов
        import time
        start_time = time.time()
        while time.time() - start_time < 60:  # 60 секунд таймаут
            pod = core_v1.read_namespaced_pod(pod_name, namespace)
            if pod.status.phase == "Running":
                break
            await asyncio.sleep(2)
        
        if pod.status.phase != "Running":
            logger.error(f"Pod {pod_name} is not running")
            return False
        
        # Копируем tarball в Pod
        copy_file_to_pod(namespace, pod_name, pathlib.Path(tarball_path), "/tmp")
        
        logger.info(f"Successfully copied tarball to pod {pod_name}")
        return True
        
    except Exception as e:
        logger.error(f"Failed to copy tarball to Job {job_name}: {e}")
        return False

async def wait_for_job_completion(job_name: str, namespace: str, timeout: int = 1800) -> bool:
    """Ожидать завершения Job"""
    import time
    
    start_time = time.time()
    while time.time() - start_time < timeout:
        try:
            job = batch_v1.read_namespaced_job(job_name, namespace)
            
            if job.status.succeeded is not None and job.status.succeeded > 0:
                logger.info(f"Job {job_name} completed successfully")
                return True
            elif job.status.failed is not None and job.status.failed > 0:
                logger.error(f"Job {job_name} failed")
                return False
            
            await asyncio.sleep(5)
            
        except Exception as e:
            logger.error(f"Error checking Job {job_name}: {e}")
            return False
    
    logger.error(f"Job {job_name} timed out after {timeout} seconds")
    return False

async def handle_configuration_create_with_job(body, spec, name, namespace, **kwargs):
    """Обработчик создания NixosConfiguration с использованием Job"""
    logger.info(f"Creating NixosConfiguration with Job: {name}")
    
    temp_dir = None
    try:
        # Получение связанной машины
        machine_name = spec['machineRef']['name']
        machine = get_machine(machine_name, namespace)
        
        # Проверка доступности машины
        is_discoverable = await check_machine_discoverable(machine['spec'], body, machine_name, namespace)
        if not is_discoverable:
            logger.warning(f"Skipping configuration application for {name}: machine {machine_name} is not discoverable")
            await update_configuration_status(
                name,
                namespace,
                {
                    "appliedCommit": None,
                    "lastAppliedTime": None,
                    "targetMachine": machine_name,
                    "conditions": [{
                        "type": "Applied",
                        "status": "False",
                        "lastTransitionTime": datetime.utcnow().isoformat() + "Z",
                        "reason": "MachineNotDiscoverable",
                        "message": "Configuration application skipped due to machine not being discoverable"
                    }]
                }
            )
            return
        
        # Подготавливаем tarball с конфигурацией
        tarball_path, commit_hash = await prepare_configuration_tarball(spec, namespace)
        temp_dir = os.path.dirname(tarball_path)
        
        # Создаем Job для применения конфигурации
        job_name = f"nixos-config-{name}-{datetime.utcnow().strftime('%Y%m%d%H%M%S')}"
        job_created = await create_nixos_job(
            job_name,
            namespace,
            machine['spec'],
            spec
        )
        
        if not job_created:
            raise Exception("Failed to create Job")
        
        # Копируем tarball в Job
        copy_success = await copy_tarball_to_job(job_name, namespace, tarball_path)
        if not copy_success:
            raise Exception("Failed to copy tarball to Job")
        
        # Ждем завершения Job
        success = await wait_for_job_completion(job_name, namespace)
        
        if success:
            # Обновление статусов
            current_time = datetime.utcnow().isoformat() + "Z"
            
            # Обновление Machine
            await update_machine_status(
                machine_name,
                namespace,
                {
                    "hasConfiguration": True,
                    "appliedConfiguration": name,
                    "appliedCommit": commit_hash,
                    "lastAppliedTime": current_time
                }
            )
            
            # Обновление NixosConfiguration
            await update_configuration_status(
                name,
                namespace,
                {
                    "appliedCommit": commit_hash,
                    "lastAppliedTime": current_time,
                    "targetMachine": machine_name
                }
            )
            
            logger.info(f"Successfully applied configuration {name} to machine {machine_name} via Job")
        else:
            raise Exception("Job failed to apply configuration")
            
    except Exception as e:
        logger.error(f"Failed to process NixosConfiguration {name}: {e}")
        raise
    finally:
        # Очистка временных файлов
        if temp_dir and os.path.exists(temp_dir):
            shutil.rmtree(temp_dir, ignore_errors=True)

async def handle_configuration_delete_with_job(body, spec, name, namespace, **kwargs):
    """Обработчик удаления NixosConfiguration с использованием Job"""
    logger.info(f"Deleting NixosConfiguration with Job: {name}")
    
    temp_dir = None
    try:
        # Получение связанной машины
        machine_name = spec['machineRef']['name']
        
        # Очистка статуса машины
        await update_machine_status(
            machine_name,
            namespace,
            {
                "hasConfiguration": False,
                "appliedConfiguration": None,
                "appliedCommit": None
            }
        )
        
        # Если указана конфигурация для удаления, применить её через Job
        if 'onRemoveFlake' in spec:
            # Подготавливаем tarball с конфигурацией удаления
            tarball_path, commit_hash = await prepare_configuration_tarball(spec, namespace)
            temp_dir = os.path.dirname(tarball_path)
            
            machine = get_machine(machine_name, namespace)
            
            # Создаем Job для применения конфигурации удаления
            job_name = f"nixos-remove-{name}-{datetime.utcnow().strftime('%Y%m%d%H%M%S')}"
            job_created = await create_nixos_job(
                job_name,
                namespace,
                machine['spec'],
                spec,
                is_remove=True
            )
            
            if job_created:
                # Копируем tarball в Job
                await copy_tarball_to_job(job_name, namespace, tarball_path)
                # Ждем завершения Job
                await wait_for_job_completion(job_name, namespace)
            
        logger.info(f"Successfully cleaned up configuration {name}")
        
    except Exception as e:
        logger.error(f"Failed to cleanup NixosConfiguration {name}: {e}")
    finally:
        # Очистка временных файлов
        if temp_dir and os.path.exists(temp_dir):
            shutil.rmtree(temp_dir, ignore_errors=True)
