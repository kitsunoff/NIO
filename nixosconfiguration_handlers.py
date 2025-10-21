#!/usr/bin/env python3

import logging
import shutil
import asyncio
from datetime import datetime
import kopf
from machine_handlers import check_machine_discoverable
from clients import get_machine, update_machine_status, update_configuration_status
from utils import clone_git_repo



logger = logging.getLogger(__name__)

async def apply_nixos_configuration(machine_spec: dict, config_spec: dict, 
                                  repo_path: str, commit_hash: str) -> bool:
    """Применить конфигурацию NixOS к машине с использованием --target-host"""
    try:
        # Определение пути к конфигурации с учетом поддиректории
        config_path = repo_path
        if config_spec.get('configurationSubdir'):
            config_path = f"{repo_path}/{config_spec['configurationSubdir']}"
        
        # Определение команды в зависимости от режима с --target-host
        if config_spec.get('fullInstall', False):
            # Полная установка с nixos-anywhere
            cmd = f"nixos-anywhere --target-host {machine_spec['ipAddress']} --kexec {config_path}/{config_spec['flake']}"
        else:
            # Обновление существующей системы с nixos-rebuild
            cmd = f"nixos-rebuild switch --flake {config_path}/{config_spec['flake']} --target-host {machine_spec['ipAddress']}"
        
        # Выполнение команды локально (внутри контейнера оператора)
        logger.info(f"Executing command: {cmd}")
        
        # Здесь мы выполняем команду локально, а не через SSH
        # Команда сама управляет подключением к целевому хосту
        process = await asyncio.create_subprocess_shell(
            cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        
        stdout, stderr = await process.communicate()
        
        if process.returncode == 0:
            logger.info(f"Configuration applied successfully: {stdout.decode()}")
            return True
        else:
            logger.error(f"Command failed with return code {process.returncode}: {stderr.decode()}")
            return False
            
    except Exception as e:
        logger.error(f"Failed to apply configuration: {e}")
        return False

async def handle_configuration_create(body, spec, name, namespace, **kwargs):
    """Обработчик создания NixosConfiguration"""
    logger.info(f"Creating NixosConfiguration: {name}")
    
    try:
        # Получение связанной машины
        machine_name = spec['machineRef']['name']
        machine = get_machine(machine_name, namespace)
        
        # Проверка доступности машины перед применением конфигурации
        is_discoverable = await check_machine_discoverable(machine['spec'], machine_name, namespace)
        if not is_discoverable:
            logger.warning(f"Skipping configuration application for {name}: machine {machine_name} is not discoverable due to missing credentials")
            # Обновление статуса конфигурации
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
                        "reason": "MissingCredentials",
                        "message": "Configuration application skipped due to missing SSH credentials"
                    }]
                }
            )
            return  # Пропускаем реконсайл
        
        # Клонирование Git репозитория
        from utils import clone_git_repo
        repo_path, commit_hash = await clone_git_repo(
            spec['gitRepo'],
            spec.get('credentialsRef'),
            namespace
        )
        
        try:
            # Применение конфигурации
            success = await apply_nixos_configuration(
                machine['spec'],
                spec,
                repo_path,
                commit_hash
            )
            
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
                
                logger.info(f"Successfully applied configuration {name} to machine {machine_name}")
                
            else:
                raise kopf.TemporaryError("Failed to apply configuration", delay=60)
                
        finally:
            # Очистка временного каталога
            shutil.rmtree(repo_path, ignore_errors=True)
            
    except Exception as e:
        logger.error(f"Failed to process NixosConfiguration {name}: {e}")
        raise kopf.TemporaryError(f"Configuration application failed: {e}", delay=60)

async def handle_configuration_delete(body, spec, name, namespace, **kwargs):
    """Обработчик удаления NixosConfiguration"""
    logger.info(f"Deleting NixosConfiguration: {name}")
    
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
        
        # Если указана конфигурация для удаления, применить её
        if 'onRemoveFlake' in spec:
            repo_path, commit_hash = await clone_git_repo(
                spec['gitRepo'],
                spec.get('credentialsRef'),
                namespace
            )
            
            try:
                machine = get_machine(machine_name, namespace)
                
                # Применение конфигурации удаления
                remove_spec = spec.copy()
                remove_spec['flake'] = spec['onRemoveFlake']
                
                await apply_nixos_configuration(
                    machine['spec'],
                    remove_spec,
                    repo_path,
                    commit_hash
                )
                
            finally:
                shutil.rmtree(repo_path, ignore_errors=True)
                
        logger.info(f"Successfully cleaned up configuration {name}")
        
    except Exception as e:
        logger.error(f"Failed to cleanup NixosConfiguration {name}: {e}")
