#!/usr/bin/env python3

import logging
import shutil
import asyncio
from datetime import datetime
import tempfile
import kopf
from machine_handlers import check_machine_discoverable
from clients import get_machine, update_machine_status, update_configuration_status
from utils import clone_git_repo
import os 
from clients import get_secret_data

logger = logging.getLogger(__name__)

async def apply_nixos_configuration(
    machine_spec: dict,
    config_spec: dict,
    repo_path: str,
    commit_hash: str,
    is_remove: bool
) -> bool:
    """Применить конфигурацию NixOS к машине. SSH-ключ опционален."""
    tmp_key_path: Optional[str] = None
    try:
        # --- Обработка SSH-ключа (опционально) ---
        ssh_key_ref = machine_spec.get("sshKeySecretRef")
        identity_option = ""

        if ssh_key_ref:
            secret_name = ssh_key_ref["name"]
            secret_namespace = ssh_key_ref.get("namespace", machine_spec.get("namespace", "default"))

            try:
                secret_data = await get_secret_data(secret_name, secret_namespace)
            except Exception as e:
                logger.error(f"Failed to fetch SSH key secret '{secret_name}' in namespace '{secret_namespace}': {e}")
                return False

            ssh_private_key = secret_data.get("ssh-privatekey")
            if not ssh_private_key:
                logger.error(f"Secret '{secret_name}' does not contain 'ssh-privatekey' key")
                return False

            # Сохраняем во временный файл
            with tempfile.NamedTemporaryFile(mode='w', prefix='ssh_key_', delete=False) as tmp:
                tmp.write(ssh_private_key.strip() + '\n')
                tmp_key_path = tmp.name
            os.chmod(tmp_key_path, 0o600)

            # Формируем опцию для SSH
            identity_option = f"-i {tmp_key_path}"
        else:
            # Без ключа — ничего не добавляем
            identity_option = ""

        # --- Подготовка параметров ---
        config_path = f"{repo_path}/{config_spec['configurationSubdir']}" if config_spec.get('configurationSubdir') else repo_path
        ssh_user = machine_spec["sshUser"]
        target_host = machine_spec.get("ipAddress") or machine_spec["hostname"]

        # Определяем флейк
        if is_remove and config_spec.get('onRemoveFlake'):
            flake = config_spec['onRemoveFlake']
        else:
            flake = config_spec['flake']

        base_nix = "nix --extra-experimental-features 'nix-command flakes'"

        if config_spec.get('fullInstall', False) and not is_remove:
            # nixos-anywhere
            cmd_parts = [
                base_nix,
                "run github:nix-community/nixos-anywhere --",
                f"--target-host {ssh_user}@{target_host}",
                f"--flake {config_path}{flake}"
            ]
            if identity_option:
                cmd_parts.append(identity_option)
            cmd = " ".join(cmd_parts)
        else:
            # nixos-rebuild
            cmd_parts = [
                base_nix,
                "shell nixpkgs#nixos-rebuild --command",
                "nixos-rebuild switch",
                f"--flake {config_path}{flake}",
                f"--target-host {ssh_user}@{target_host}",
                "--ssh-option StrictHostKeyChecking=no"
            ]
            if identity_option:
                cmd_parts.append(f"--ssh-option IdentityFile={tmp_key_path}")
            cmd = " ".join(cmd_parts)

        logger.info(f"Executing command: {cmd}")

        process = await asyncio.create_subprocess_shell(
            cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        stdout, stderr = await process.communicate()

        if process.returncode != 0:
            logger.error(f"Command failed (code {process.returncode})")
            logger.error(f"stderr: {stderr.decode()}")
            logger.error(f"stdout: {stdout.decode()}")
            return False

        logger.info("NixOS configuration applied successfully")
        return True

    except Exception as e:
        logger.error(f"Unexpected error in apply_nixos_configuration: {e}")
        return False

    finally:
        if tmp_key_path and os.path.exists(tmp_key_path):
            os.unlink(tmp_key_path)

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
                commit_hash,
                False
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
                    commit_hash,
                    True
                )
                
            finally:
                shutil.rmtree(repo_path, ignore_errors=True)
                
        logger.info(f"Successfully cleaned up configuration {name}")
        
    except Exception as e:
        logger.error(f"Failed to cleanup NixosConfiguration {name}: {e}")
