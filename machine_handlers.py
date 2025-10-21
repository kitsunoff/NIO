#!/usr/bin/env python3

import logging
import asyncio
import asyncssh
import json
import tempfile
import os
from typing import Dict
from datetime import datetime
from clients import get_secret_data
from events import emit_missing_credentials_event

logger = logging.getLogger(__name__)

async def check_machine_discoverable(machine_spec: Dict, body=None, machine_name: str = None, namespace: str = None) -> bool:
    """Проверить доступность машины через SSH с поддержкой ключа, пароля и без аутентификации"""
    try:
        ssh_config = {
            'host': machine_spec['hostname'],
            'username': machine_spec.get('sshUser', 'root'),
            'known_hosts': None  # Отключить проверку known hosts
        }
        
        has_credentials = False
        ssh_key_temp_file = None
        
        # Попытка подключения по SSH ключу
        if 'sshKeySecretRef' in machine_spec:
            try:
                secret_data = await get_secret_data(
                    machine_spec['sshKeySecretRef']['name'],
                    machine_spec['sshKeySecretRef'].get('namespace', 'default')
                )
                if 'ssh-privatekey' in secret_data and secret_data['ssh-privatekey']:
                    # Создаем временный файл для SSH ключа
                    with tempfile.NamedTemporaryFile(mode='w', delete=False, suffix='_ssh_key') as temp_file:
                        temp_file.write(secret_data['ssh-privatekey'])
                        ssh_key_temp_file = temp_file.name
                    
                    # Устанавливаем правильные права доступа для SSH ключа
                    os.chmod(ssh_key_temp_file, 0o600)
                    
                    ssh_config['client_keys'] = [ssh_key_temp_file]
                    has_credentials = True
                    logger.info("Using SSH key for authentication")
                else:
                    # Секрет существует, но не содержит SSH ключа
                    if body:
                        emit_missing_credentials_event(
                            body, 
                            "MissingSSHKey",
                            f"Secret {machine_spec['sshKeySecretRef']['name']} exists but doesn't contain 'ssh-privatekey'"
                        )
                    logger.warning(f"Secret {machine_spec['sshKeySecretRef']['name']} exists but doesn't contain 'ssh-privatekey'")
            except Exception as e:
                # Секрет не найден или недоступен
                if body:
                    emit_missing_credentials_event(
                        body,
                        "SecretNotFound",
                        f"Failed to get SSH key from secret {machine_spec['sshKeySecretRef']['name']}"
                    )
                logger.warning(f"Failed to get SSH key from secret {machine_spec['sshKeySecretRef']['name']}: {e}")
        
        # Попытка подключения по паролю (если ключ не сработал или не указан)
        if not has_credentials and 'sshPasswordSecretRef' in machine_spec:
            try:
                secret_data = await get_secret_data(
                    machine_spec['sshPasswordSecretRef']['name'],
                    machine_spec['sshPasswordSecretRef'].get('namespace', 'default')
                )
                
                # Определяем ключ для пароля (по умолчанию 'password')
                password_key = machine_spec['sshPasswordSecretRef'].get('key', 'password')
                
                if password_key in secret_data and secret_data[password_key]:
                    ssh_config['password'] = secret_data[password_key]
                    has_credentials = True
                    logger.info("Using password for authentication")
                else:
                    # Секрет существует, но не содержит пароля
                    if body:
                        emit_missing_credentials_event(
                            body, 
                            "MissingPassword",
                            f"Secret {machine_spec['sshPasswordSecretRef']['name']} exists but doesn't contain '{password_key}'"
                        )
                    logger.warning(f"Secret {machine_spec['sshPasswordSecretRef']['name']} exists but doesn't contain '{password_key}'")
            except Exception as e:
                # Секрет не найден или недоступен
                if body:
                    emit_missing_credentials_event(
                        body,
                        "SecretNotFound",
                        f"Failed to get password from secret {machine_spec['sshPasswordSecretRef']['name']}"
                    )
                logger.warning(f"Failed to get password from secret {machine_spec['sshPasswordSecretRef']['name']}: {e}")
        
        # Если нет указанных учетных данных, пробуем подключиться без аутентификации
        if not has_credentials:
            logger.info("No SSH key or password provided, attempting connection without authentication")
            # Продолжаем без дополнительных параметров аутентификации
        
        # Попытка подключения
        try:
            async with asyncssh.connect(**ssh_config) as conn:
                # Простая команда для проверки доступности
                result = await conn.run('echo "machine_available"', check=True)
                return result.stdout.strip() == "machine_available"
        finally:
            # Удаляем временный файл SSH ключа, если он был создан
            if ssh_key_temp_file and os.path.exists(ssh_key_temp_file):
                try:
                    os.unlink(ssh_key_temp_file)
                    logger.debug(f"Deleted temporary SSH key file: {ssh_key_temp_file}")
                except Exception as e:
                    logger.warning(f"Failed to delete temporary SSH key file {ssh_key_temp_file}: {e}")
            
    except Exception as e:
        logger.warning(f"Machine {machine_spec.get('hostname')} is not discoverable: {e}")
        return False


async def scan_machine_hardware(machine_spec: Dict, body=None, machine_name: str = None, namespace: str = None) -> Dict:
    """Сканировать железо машины и вернуть факты"""
    try:
        ssh_config = {
            'host': machine_spec['hostname'],
            'username': machine_spec.get('sshUser', 'root'),
            'known_hosts': None  # Отключить проверку known hosts
        }
        
        has_credentials = False
        ssh_key_temp_file = None
        
        # Попытка подключения по SSH ключу
        if 'sshKeySecretRef' in machine_spec:
            try:
                secret_data = await get_secret_data(
                    machine_spec['sshKeySecretRef']['name'],
                    machine_spec['sshKeySecretRef'].get('namespace', 'default')
                )
                if 'ssh-privatekey' in secret_data and secret_data['ssh-privatekey']:
                    # Создаем временный файл для SSH ключа
                    with tempfile.NamedTemporaryFile(mode='w', delete=False, suffix='_ssh_key') as temp_file:
                        temp_file.write(secret_data['ssh-privatekey'])
                        ssh_key_temp_file = temp_file.name
                    
                    # Устанавливаем правильные права доступа для SSH ключа
                    os.chmod(ssh_key_temp_file, 0o600)
                    
                    ssh_config['client_keys'] = [ssh_key_temp_file]
                    has_credentials = True
                    logger.info("Using SSH key for hardware scan")
            except Exception as e:
                logger.warning(f"Failed to get SSH key for hardware scan: {e}")
        
        # Попытка подключения по паролю (если ключ не сработал или не указан)
        if not has_credentials and 'sshPasswordSecretRef' in machine_spec:
            try:
                secret_data = await get_secret_data(
                    machine_spec['sshPasswordSecretRef']['name'],
                    machine_spec['sshPasswordSecretRef'].get('namespace', 'default')
                )
                
                # Определяем ключ для пароля (по умолчанию 'password')
                password_key = machine_spec['sshPasswordSecretRef'].get('key', 'password')
                
                if password_key in secret_data and secret_data[password_key]:
                    ssh_config['password'] = secret_data[password_key]
                    has_credentials = True
                    logger.info("Using password for hardware scan")
            except Exception as e:
                logger.warning(f"Failed to get password for hardware scan: {e}")
        
        # Если нет указанных учетных данных, пробуем подключиться без аутентификации
        if not has_credentials:
            logger.info("No SSH key or password provided, attempting hardware scan without authentication")
        
        # Подключение и выполнение сканирования
        try:
            async with asyncssh.connect(**ssh_config) as conn:
                # Передаем скрипт сканирования по SCP
                scanner_path = os.path.join(os.path.dirname(__file__), 'scripts', 'hardware_scanner.sh')
                
                if not os.path.exists(scanner_path):
                    logger.error(f"Hardware scanner script not found at {scanner_path}")
                    return {}
                
                # Читаем содержимое скрипта
                with open(scanner_path, 'r') as f:
                    scanner_content = f.read()
                
                # Создаем временный файл на удаленной машине
                remote_script_path = "/tmp/hardware_scanner.sh"
                
                # Передаем скрипт по SCP
                async with conn.start_sftp_client() as sftp:
                    async with sftp.open(remote_script_path, 'w') as remote_file:
                        await remote_file.write(scanner_content)
                
                # Делаем скрипт исполняемым и выполняем его
                await conn.run(f"chmod +x {remote_script_path}", check=True)
                result = await conn.run(f"{remote_script_path}", check=True)
                
                # Получаем сырой вывод сканера
                facts_output = result.stdout.strip()
                if not facts_output:
                    logger.warning("Hardware scanner returned empty output")
                    return {}
                
                # Парсим результат локально
                from scripts.facts_parser import parse_facts
                
                # Разбиваем вывод на строки и парсим
                lines = facts_output.split('\n')
                hardware_facts = parse_facts(lines)
                
                logger.info(f"Successfully scanned hardware for machine {machine_spec['hostname']}")
                return hardware_facts
        finally:
            # Удаляем временный файл SSH ключа, если он был создан
            if ssh_key_temp_file and os.path.exists(ssh_key_temp_file):
                try:
                    os.unlink(ssh_key_temp_file)
                    logger.debug(f"Deleted temporary SSH key file: {ssh_key_temp_file}")
                except Exception as e:
                    logger.warning(f"Failed to delete temporary SSH key file {ssh_key_temp_file}: {e}")
            
    except Exception as e:
        logger.warning(f"Failed to scan hardware for machine {machine_spec.get('hostname')}: {e}")
        return {}
