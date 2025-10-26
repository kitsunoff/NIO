#!/usr/bin/env python3

import logging
import shutil
import asyncio
from datetime import datetime
import tempfile
import kopf
import os
import json
import hashlib
from typing import Dict, Optional

from machine_handlers import check_machine_discoverable
from clients import get_machine, update_machine_status, update_configuration_status, get_secret_data
from utils import clone_git_repo, calculate_directory_hash, get_workdir_path, parse_flake_reference, extract_repo_name_from_url, get_remote_commit_hash

logger = logging.getLogger(__name__)


async def inject_additional_files(repo_path: str, config_spec: dict, namespace: str, machine_spec: Optional[dict] = None) -> str:
    """Инжектировать additionalFiles в configurationSubdir и вернуть хеш директории"""
    if not config_spec.get('additionalFiles'):
        return calculate_directory_hash(repo_path)
    
    config_subdir = config_spec.get('configurationSubdir', '')
    base_path = os.path.join(repo_path, config_subdir) if config_subdir else repo_path
    
    for file_spec in config_spec['additionalFiles']:
        file_path = os.path.join(base_path, file_spec['path'])
        file_dir = os.path.dirname(file_path)
        
        # Создаем директорию если нужно
        os.makedirs(file_dir, exist_ok=True)
        
        # Обрабатываем разные типы значений
        value_type = file_spec['valueType']
        
        if value_type == 'Inline':
            content = file_spec.get('inline', '')
            with open(file_path, 'w') as f:
                f.write(content)
            logger.info(f"Injected inline file: {file_spec['path']}")
            
        elif value_type == 'SecretRef':
            secret_ref = file_spec.get('secretRef', {})
            secret_name = secret_ref.get('name')
            if not secret_name:
                logger.warning(f"Missing secret name for file {file_spec['path']}")
                continue
                
            try:
                secret_data = await get_secret_data(secret_name, namespace)
                # Используем первый ключ из secret или указанный ключ
                if secret_data:
                    first_key = next(iter(secret_data.keys()))
                    content = secret_data[first_key]
                    with open(file_path, 'w') as f:
                        f.write(content)
                    logger.info(f"Injected secret file: {file_spec['path']} from secret {secret_name}")
                else:
                    logger.warning(f"Empty secret {secret_name} for file {file_spec['path']}")
            except Exception as e:
                logger.error(f"Failed to inject secret file {file_spec['path']}: {e}")
                
        elif value_type == 'NixosFacter':
            if not machine_spec:
                logger.warning(f"Cannot generate NixosFacter for {file_spec['path']}: no machine spec")
                continue
                
            # Генерация фактов NixOS
            facts = generate_nixos_facts(machine_spec)
            content = json.dumps(facts, indent=2)
            with open(file_path, 'w') as f:
                f.write(content)
            logger.info(f"Generated NixosFacter file: {file_spec['path']}")
    
    # Возвращаем хеш директории после инъекции
    return calculate_directory_hash(base_path)


def generate_nixos_facts(machine_spec: dict) -> dict:
    """Генерирует факты NixOS для машины"""
    facts = {
        "machine-id": machine_spec.get('hostname', 'unknown'),
        "hostname": machine_spec.get('hostname', 'unknown'),
        "ip-address": machine_spec.get('ipAddress', 'unknown'),
    }
    
    # Добавляем hardware facts если есть
    if machine_spec.get('status', {}).get('hardwareFacts'):
        facts.update(machine_spec['status']['hardwareFacts'])
    
    return facts


def get_configuration_hash(config_spec: dict, repo_path: str, namespace: str, machine_spec: Optional[dict] = None) -> str:
    """Вычисляет хеш конфигурации для проверки идемпотентности"""
    hash_obj = hashlib.sha256()
    
    # Добавляем flake в хеш
    hash_obj.update(config_spec.get('flake', '').encode('utf-8'))
    
    # Добавляем хеш директории configurationSubdir
    config_subdir = config_spec.get('configurationSubdir', '')
    config_path = os.path.join(repo_path, config_subdir) if config_subdir else repo_path
    dir_hash = calculate_directory_hash(config_path)
    hash_obj.update(dir_hash.encode('utf-8'))
    
    # Добавляем additionalFiles спецификацию
    if config_spec.get('additionalFiles'):
        files_spec = json.dumps(config_spec['additionalFiles'], sort_keys=True)
        hash_obj.update(files_spec.encode('utf-8'))
    
    return hash_obj.hexdigest()

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

        # Асинхронное чтение stdout и stderr в реальном времени
        async def read_stream(stream, log_func):
            if stream:
                async for line in stream:
                    decoded = line.decode('utf-8', errors='replace').rstrip()
                    if decoded:
                        log_func(decoded)

        # Запускаем чтение stdout и stderr параллельно
        await asyncio.gather(
            read_stream(process.stdout, logger.info),
            read_stream(process.stderr, logger.error),
            process.wait()  # ждём завершения процесса
        )

        if process.returncode != 0:
            logger.error(f"Command failed (code {process.returncode})")
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

async def handle_configuration_update(body, spec, name, namespace, **kwargs):
    """Обработчик обновления NixosConfiguration"""
    logger.info(f"Updating NixosConfiguration: {name}")
    
    try:
        # Получение связанной машины
        machine_name = spec['machineRef']['name']
        machine = get_machine(machine_name, namespace)
        
        # Проверка доступности машины
        is_discoverable = await check_machine_discoverable(machine['spec'], machine_name, namespace)
        if not is_discoverable:
            logger.warning(f"Skipping configuration update for {name}: machine {machine_name} is not discoverable")
            await update_configuration_status(
                name,
                namespace,
                {
                    "conditions": [{
                        "type": "Applied",
                        "status": "False",
                        "lastTransitionTime": datetime.utcnow().isoformat() + "Z",
                        "reason": "MachineNotDiscoverable",
                        "message": "Configuration update skipped due to machine not being discoverable"
                    }]
                }
            )
            return
        
        # Определяем источник конфигурации
        repo_name, repo_url, commit_hash = parse_flake_reference(spec['flake'])
        if commit_hash == "floating":
            # Для плавающих ссылок используем gitRepo
            repo_url = spec['gitRepo']
            repo_name = extract_repo_name_from_url(repo_url)
            # Получаем актуальный коммит для ветки/тега
            flake_parts = spec['flake'].split('#')[0].split('/')
            ref = flake_parts[2] if len(flake_parts) > 2 else "main"
            commit_hash = await get_remote_commit_hash(repo_url, ref, spec.get('credentialsRef'), namespace)
        
        # Создаем предсказуемый путь
        workdir_path = get_workdir_path(namespace, name, repo_name, commit_hash)
        
        # Клонируем репозиторий в предсказуемый путь
        repo_path, actual_commit_hash = await clone_git_repo(
            repo_url,
            spec.get('credentialsRef'),
            namespace,
            target_path=workdir_path
        )
        
        try:
            # Инжектируем additionalFiles
            config_hash = await inject_additional_files(repo_path, spec, namespace, machine['spec'])
            
            # Проверяем идемпотентность
            current_config_hash = get_configuration_hash(spec, repo_path, namespace, machine['spec'])
            
            # Получаем текущий статус для проверки необходимости применения
            current_status = body.get('status', {})
            current_applied_hash = current_status.get('configurationHash')
            
            if current_applied_hash == current_config_hash:
                logger.info(f"Configuration {name} unchanged, skipping application")
                return
            
            # Применяем конфигурацию
            success = await apply_nixos_configuration(
                machine['spec'],
                spec,
                repo_path,
                actual_commit_hash,
                False
            )
            
            if success:
                # Обновляем статусы
                current_time = datetime.utcnow().isoformat() + "Z"
                
                await update_machine_status(
                    machine_name,
                    namespace,
                    {
                        "hasConfiguration": True,
                        "appliedConfiguration": name,
                        "appliedCommit": actual_commit_hash,
                        "lastAppliedTime": current_time
                    }
                )
                
                await update_configuration_status(
                    name,
                    namespace,
                    {
                        "appliedCommit": actual_commit_hash,
                        "lastAppliedTime": current_time,
                        "targetMachine": machine_name,
                        "configurationHash": current_config_hash,
                        "conditions": [{
                            "type": "Applied",
                            "status": "True",
                            "lastTransitionTime": current_time,
                            "reason": "Success",
                            "message": "Configuration successfully applied"
                        }]
                    }
                )
                
                logger.info(f"Successfully updated configuration {name} to machine {machine_name}")
                
                # Запускаем GC для старых версий
                await garbage_collect_old_versions(namespace, name, workdir_path)
                
            else:
                raise kopf.TemporaryError("Failed to apply configuration", delay=60)
                
        except Exception as e:
            logger.error(f"Failed to process configuration update: {e}")
            raise kopf.TemporaryError(f"Configuration update failed: {e}", delay=60)
            
    except Exception as e:
        logger.error(f"Failed to update NixosConfiguration {name}: {e}")
        raise kopf.TemporaryError(f"Configuration update failed: {e}", delay=60)


async def handle_configuration_resume(body, spec, name, namespace, **kwargs):
    """Обработчик возобновления NixosConfiguration после перезапуска оператора"""
    logger.info(f"Resuming NixosConfiguration: {name}")
    
    # При возобновлении просто запускаем обновление
    await handle_configuration_update(body, spec, name, namespace, **kwargs)


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


async def garbage_collect_old_versions(namespace: str, name: str, current_path: str):
    """Удалить старые версии конфигурации, оставив только текущую"""
    base_dir = os.path.dirname(current_path)
    if not os.path.exists(base_dir):
        return
    
    for item in os.listdir(base_dir):
        item_path = os.path.join(base_dir, item)
        if item_path != current_path and os.path.isdir(item_path):
            try:
                shutil.rmtree(item_path)
                logger.info(f"Garbage collected old configuration: {item_path}")
            except Exception as e:
                logger.warning(f"Failed to garbage collect {item_path}: {e}")


@kopf.timer('nixos.infra', 'v1alpha1', 'nixosconfigurations', interval=3600.0)
async def garbage_collect_all_old_configurations(**kwargs):
    """Фоновый GC для всех конфигураций старше 24 часов"""
    base_path = "/tmp/nixos-config"
    if not os.path.exists(base_path):
        return
    
    current_time = datetime.now().timestamp()
    
    for namespace in os.listdir(base_path):
        namespace_path = os.path.join(base_path, namespace)
        if not os.path.isdir(namespace_path):
            continue
            
        for config_name in os.listdir(namespace_path):
            config_path = os.path.join(namespace_path, config_name)
            if not os.path.isdir(config_path):
                continue
                
            for version_dir in os.listdir(config_path):
                version_path = os.path.join(config_path, version_dir)
                if not os.path.isdir(version_path):
                    continue
                
                # Проверяем время модификации
                try:
                    stat = os.stat(version_path)
                    age_hours = (current_time - stat.st_mtime) / 3600
                    
                    if age_hours > 24:
                        shutil.rmtree(version_path)
                        logger.info(f"GC: Removed old configuration {version_path} (age: {age_hours:.1f}h)")
                except Exception as e:
                    logger.warning(f"GC: Failed to remove {version_path}: {e}")


@kopf.timer('nixos.infra', 'v1alpha1', 'nixosconfigurations', interval=300.0)
async def check_floating_references(body, spec, name, namespace, **kwargs):
    """Проверка обновлений для плавающих ссылок (ветки/теги)"""
    flake_ref = spec.get('flake', '')
    if not flake_ref:
        return
    
    repo_name, repo_url, commit_type = parse_flake_reference(flake_ref)
    if commit_type != "floating":
        return  # Только для плавающих ссылок
    
    # Получаем текущий HEAD для ветки/тега
    flake_parts = flake_ref.split('#')[0].split('/')
    ref = flake_parts[2] if len(flake_parts) > 2 else "main"
    
    try:
        current_head = await get_remote_commit_hash(repo_url, ref, spec.get('credentialsRef'), namespace)
        
        # Проверяем, изменился ли HEAD
        current_status = body.get('status', {})
        current_commit = current_status.get('appliedCommit')
        
        if current_commit and current_commit != current_head:
            logger.info(f"Floating reference {flake_ref} changed from {current_commit} to {current_head}, triggering reconcile")
            # Запускаем полный reconcile
            await handle_configuration_update(body, spec, name, namespace, **kwargs)
            
    except Exception as e:
        logger.warning(f"Failed to check floating reference {flake_ref}: {e}")
