#!/usr/bin/env python3
"""
PXE-сервер с регистрацией машин в Kubernetes и отдачей netboot.ipxe из .pxe/result
"""

import os
import sys
import signal
import logging
import subprocess
import threading
import argparse
from pathlib import Path
from typing import Optional

import uvicorn
from fastapi import FastAPI, Request, Response, HTTPException
from kubernetes import client, config
import kubernetes
from kubernetes.client.rest import ApiException

# ==============================
# Настройки
# ==============================
BASE_DIR = Path.cwd() / ".pxe"
RESULT_DIR = BASE_DIR / "result"
SSH_DIR = BASE_DIR / "ssh"
TFTP_ROOT = BASE_DIR / "tftp"
HTTP_PORT = 8000

# Логирование
logging.basicConfig(
    level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s"
)
logger = logging.getLogger("pxe-k8s")

# Глобальные переменные
dnsmasq_proc: Optional[subprocess.Popen] = None
GROUP, VERSION, PLURAL = "nixos.infra", "v1alpha1", "machines"
crd_api = None
REGISTERED_MACHINES = set()


# ==============================
# Утилиты
# ==============================
def get_primary_interface_and_ip():
    """Простое определение основного интерфейса и IP (Linux/macOS)"""
    try:
        # Linux
        route_output = subprocess.check_output(["ip", "route", "get", "1"], text=True)
        iface = route_output.split()[route_output.split().index("dev") + 1]
        addr_output = subprocess.check_output(["ip", "addr", "show", iface], text=True)
        for line in addr_output.splitlines():
            if "inet " in line and "127.0.0.1" not in line:
                ip = line.split()[1].split("/")[0]
                return iface, ip
    except (subprocess.CalledProcessError, FileNotFoundError):
        pass

    try:
        # macOS
        for interface in ["en0", "en1", "en2"]:
            ip = subprocess.check_output(
                ["ipconfig", "getifaddr", interface], text=True
            ).strip()
            if ip:
                return interface, ip
    except (subprocess.CalledProcessError, FileNotFoundError):
        pass

    logger.error("Не удалось определить сетевой интерфейс")
    return None, None


def ensure_ipxe_binaries():
    """Скачивает необходимые файлы iPXE"""
    TFTP_ROOT.mkdir(exist_ok=True)
    files = {
        "undionly.kpxe": "https://boot.ipxe.org/undionly.kpxe  ",
        "ipxe.efi": "https://boot.ipxe.org/ipxe.efi  ",
    }
    for name, url in files.items():
        dst = TFTP_ROOT / name
        if not dst.exists():
            logger.info(f"Скачиваю {name}...")
            import urllib.request

            urllib.request.urlretrieve(url, dst)


def generate_ssh_keys_if_missing():
    """Генерирует SSH-ключи, если они не существуют."""
    SSH_DIR.mkdir(exist_ok=True)
    private_key_path = SSH_DIR / "id_rsa"
    public_key_path = SSH_DIR / "id_rsa.pub"

    if private_key_path.exists() and public_key_path.exists():
        logger.info(f"SSH-ключи уже существуют: {private_key_path}, {public_key_path}")
        return str(private_key_path), str(public_key_path)

    logger.info("SSH-ключи не найдены, генерирую новые...")
    try:
        subprocess.run(
            [
                "ssh-keygen",
                "-t",
                "rsa",
                "-b",
                "4096",
                "-f",
                str(private_key_path),
                "-N",
                "",
            ],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        logger.info(f"SSH-ключи сгенерированы: {private_key_path}, {public_key_path}")
        return str(private_key_path), str(public_key_path)
    except subprocess.CalledProcessError as e:
        logger.error(f"Ошибка генерации SSH-ключей: {e}")
        sys.exit(1)
    except FileNotFoundError:
        logger.error(
            "Команда ssh-keygen не найдена. Убедитесь, что OpenSSH установлен."
        )
        sys.exit(1)


def build_nixos_netboot_if_missing(public_key_path: str):
    """Проверяет наличие netboot-файлов и собирает их, если они отсутствуют.
    Использует кастомную конфигурацию с SSH-ключом.
    """
    netboot_ipxe_path = RESULT_DIR / "netboot.ipxe"

    required_files_exist = netboot_ipxe_path.exists()

    if required_files_exist:
        logger.info("Файлы netboot уже существуют в .pxe/result, сборка не требуется.")
        return

    logger.info("Файлы netboot не найдены, запускаю сборку...")

    # --- Добавляем путь к Nix в PATH ---
    nix_bin_path = "/nix/var/nix/profiles/default/bin"
    current_path = os.environ.get("PATH", "")
    if nix_bin_path not in current_path:
        os.environ["PATH"] = f"{nix_bin_path}:{current_path}"
        logger.info(f"Добавлен путь к Nix в PATH: {nix_bin_path}")

    # Путь к кастомной конфигурации
    custom_config_path = BASE_DIR / "configuration.nix"

    # Проверяем, существует ли файл конфигурации
    if not custom_config_path.exists():
        logger.info(
            f"Файл конфигурации {custom_config_path} не найден, создаю стандартный..."
        )
        # Читаем содержимое публичного ключа
        try:
            public_key_content = Path(public_key_path).read_text().strip()
            if not public_key_content.startswith("ssh-"):
                logger.error(
                    f"Файл {public_key_path} не содержит валидный публичный ключ SSH."
                )
                public_key_content = ""
        except Exception as e:
            logger.error(f"Ошибка чтения публичного ключа из {public_key_path}: {e}")
            public_key_content = ""

        # Содержимое стандартной конфигурации
        config_content = f"""{{ modulesPath, ... }}: {{
  imports = [ (modulesPath + "/installer/netboot/netboot-minimal.nix") ];

  services.openssh.enable = true;
  users.users.root.openssh.authorizedKeys.keys = [
    {f'"{public_key_content}"' if public_key_content else ''}
  ];
}}
"""
        custom_config_path.write_text(config_content)
        logger.info(f"Создан стандартный файл конфигурации: {custom_config_path}")

    # ...
    logger.info(f"Собираю netboot с конфигурацией: {custom_config_path}")
    nix_expression = f"""
with import <nixpkgs/nixos/release.nix> {{ configuration = import {custom_config_path}; }};
netboot.x86_64-linux
"""

    cmd = ["nix-build", "-E", nix_expression, "-o", str(RESULT_DIR)]
    logger.info(f"Выполняю: nix-build -E '<custom_expression>' -o {RESULT_DIR}")

    try:
        process = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
            universal_newlines=True,
        )

        if process.stdout:
            for line in process.stdout:
                logger.info(f"nix-build: {line.strip()}")

        process.wait()

        if process.returncode != 0:
            raise subprocess.CalledProcessError(process.returncode, cmd)

        # После сборки файлы находятся в result/
        # Проверяем, создались ли файлы после сборки
        if not netboot_ipxe_path.exists():
            logger.warning(
                f"После сборки netboot файл netboot.ipxe не найден в .pxe/result"
            )
            # Попробуем найти и скопировать его из результата сборки
            import shutil

            for item in RESULT_DIR.iterdir():
                if item.is_symlink() and (item / "netboot.ipxe").exists():
                    source_ipxe = item / "netboot.ipxe"
                    logger.info(f"Найден netboot.ipxe в {source_ipxe}, копирую...")
                    shutil.copy2(source_ipxe, netboot_ipxe_path)
                    break

        logger.info(f"Сборка netboot завершена.")

    except subprocess.CalledProcessError as e:
        logger.error(f"Ошибка сборки netboot: {e}")
        sys.exit(1)
    except FileNotFoundError:
        logger.error("Команда nix-build не найдена. Убедитесь, что Nix установлен.")
        sys.exit(1)
    except Exception as e:
        logger.error(f"Неожиданная ошибка при сборке netboot: {e}")
        sys.exit(1)


def generate_dnsmasq_conf(interface: str, server_ip: str, tftp_root: Path, dhcp_range: str) -> str:
    conf = f"""
interface={interface}
bind-interfaces

dhcp-range={dhcp_range}

# TFTP только для начальной загрузки НЕ-iPXE клиентов
enable-tftp
tftp-root={tftp_root}

# Определяем iPXE клиентов
dhcp-userclass=set:ipxe,iPXE
dhcp-match=set:ipxe,175,#iPXE

# Для НЕ-iPXE клиентов: загружаем ipxe.efi по TFTP
dhcp-boot=tag:!ipxe,ipxe.efi

# Для iPXE клиентов: принудительно используем HTTP и БЛОКИРУЕМ TFTP
dhcp-option=tag:ipxe,66,192.168.2.121
dhcp-option=tag:ipxe,67,http://{server_ip}:8000/boot.ipxe
dhcp-option=tag:ipxe,60,"iPXE"
pxe-service=tag:ipxe,X86-64_EFI,"iPXE",http://{server_ip}:8000/boot.ipxe

log-dhcp
"""
    # Сохраняем в .pxe/dnsmasq.conf (удобно для отладки)
    conf_path = BASE_DIR / "dnsmasq.conf"
    conf_path.write_text(conf)
    return str(conf_path)


def start_dnsmasq(interface: str, server_ip: str, dhcp_range: str):
    """Запускает dnsmasq в отдельном процессе"""
    global dnsmasq_proc
    conf_path = generate_dnsmasq_conf(interface, server_ip, TFTP_ROOT, dhcp_range)
    cmd = ["dnsmasq", "--no-daemon", "--conf-file=" + conf_path, "--log-dhcp"]
    logger.info("Запуск dnsmasq...")
    dnsmasq_proc = subprocess.Popen(
        cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT
    )

    def log_output():
        for line in iter(dnsmasq_proc.stdout.readline, b""):
            logger.info(f"dnsmasq: {line.decode().strip()}")

    threading.Thread(target=log_output, daemon=True).start()


def register_machine_in_k8s(mac: str, ip: str) -> str:
    """Регистрирует машину в Kubernetes и создаёт Secret с SSH-ключом."""
    mac_norm = mac.replace(":", "-").lower()
    machine_name = f"machine-{mac_norm}"
    secret_name = f"ssh-private-key-{mac_norm}"

    if mac_norm in REGISTERED_MACHINES:
        logger.info(f"Машина {machine_name} уже зарегистрирована в этой сессии")
        return machine_name

    # 1. Читаем приватный ключ
    try:
        private_key_content = Path(private_key_path).read_text()
    except Exception as e:
        logger.error(f"Ошибка чтения приватного ключа {private_key_path}: {e}")
        raise HTTPException(500, "Failed to read private key")

    # 2. Создаём Secret
    secret_body = {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {
            "name": secret_name,
            "namespace": "default",
        },
        "type": "Opaque",
        "data": {
            # Ключи в Secret должны быть в base64
            "ssh-privatekey": subprocess.check_output(
                ["base64", "-w", "0"], input=private_key_content.encode()
            )
            .decode()
            .strip()
        },
    }

    try:
        core_api.create_namespaced_secret("default", secret_body)
        logger.info(f"Secret {secret_name} создан в K8s")
    except ApiException as e:
        if e.status != 409:  # 409 = уже существует
            logger.error(f"Ошибка создания Secret {secret_name}: {e}")
            raise HTTPException(500, "Secret creation failed")
        else:
            logger.info(f"Secret {secret_name} уже существует в K8s")

    # 3. Создаём объект Machine
    machine_body = {
        "apiVersion": f"{GROUP}/{VERSION}",
        "kind": "Machine",
        "metadata": {"name": machine_name, "namespace": "default"},
        "spec": {
            "hostname": ip,
            "sshUser": "root",  # Или передавать как аргумент, если нужно
            "macAddress": mac,
            "sshKeySecretRef": {"name": secret_name, "namespace": "default"},
        },
    }

    try:
        crd_api.create_namespaced_custom_object(
            GROUP, VERSION, "default", PLURAL, machine_body
        )
        logger.info(f"Машина {machine_name} зарегистрирована в K8s")
        REGISTERED_MACHINES.add(mac_norm)
    except ApiException as e:
        if e.status != 409:
            logger.error(f"Ошибка K8s при создании Machine: {e}")
            # Если Machine не создалась, но Secret был, возможно, стоит удалить Secret?
            # Пока что просто бросаем ошибку.
            raise HTTPException(500, "Machine registration failed")
        else:
            logger.info(f"Машина {machine_name} уже существует в K8s")
            REGISTERED_MACHINES.add(mac_norm)

    return machine_name


# ==============================
# HTTP-сервер
# ==============================
app = FastAPI(title="PXE + K8s Registrar")


@app.get("/boot.ipxe")
async def boot_script(request: Request, mac: Optional[str] = None):
    """Первый скрипт iPXE - регистрирует машину и отдаёт netboot.pxe"""

    script = f"""#!ipxe
dhcp
chain http://{server_ip}:{HTTP_PORT}/netboot.pxe?mac=${{mac}}&ip=${{ip}}
"""
    return Response(content=script, media_type="text/plain")


@app.get("/netboot.pxe")
async def netboot_script(
    request: Request, mac: Optional[str] = None, ip: Optional[str] = None
):
    """Отдаёт файл netboot.ipxe из .pxe/result с подставленными MAC и IP"""
    logger.info(f"Запрос netboot.pxe от {request.client.host} с MAC {mac}, IP {ip}")

    if not mac or not ip:
        raise HTTPException(400, "MAC и IP обязательны")

    machine_name = register_machine_in_k8s(mac, ip)  # Вызываем с ключом

    netboot_file_path = RESULT_DIR / "netboot.ipxe"

    if not netboot_file_path.exists():
        logger.error(f"Файл netboot.ipxe не найден: {netboot_file_path}")
        raise HTTPException(404, "Файл netboot.ipxe не найден в .pxe/result")

    try:
        content = netboot_file_path.read_text()
        # Подставляем HTTP-пути к kernel и initrd
        # Заменяем только имена файлов, оставляя остальные параметры без изменений
        content = content.replace(
            "bzImage", f"http://{server_ip}:{HTTP_PORT}/result/bzImage"
        )
        # Заменяем "initrd=initrd" на "initrd=http://..."
        # И "initrd initrd" на "initrd http://..."
        # Можно сделать это одной строкой, заменив "initrd" на полный путь, но будь осторожен с init=/nix/store/...
        # Лучше заменить конкретные вхождения "initrd" как имя файла, а не как часть параметра init=.
        # Например, можно сначала заменить " initrd " (с пробелами), чтобы не трогать init=/nix/store...
        content = content.replace(
            " initrd ", f" http://{server_ip}:{HTTP_PORT}/result/initrd "
        )
        # Затем заменить "initrd initrd" на "initrd http://..."
        content = content.replace(
            "initrd initrd", f"initrd http://{server_ip}:{HTTP_PORT}/result/initrd"
        )
        # Или, проще и надёжнее, заменить все вхождения "initrd" на полный путь, но только если это отдельное слово или после/до него пробел/новая строка
        # Для простоты и точности, заменим построчно
        lines = content.splitlines()
        processed_lines = []
        for line in lines:
            # Обрабатываем строку kernel
            if line.strip().startswith("kernel"):
                line = line.replace(
                    " bzImage ", f" http://{server_ip}:{HTTP_PORT}/result/bzImage "
                )
                # Заменяем initrd=initrd на initrd=...URL
                import re

                # Заменяем initrd=initrd на initrd=...URL, но только если это отдельное слово после =
                line = re.sub(
                    r"(\binitrd=)initrd\b",
                    rf"\g<1>http://{server_ip}:{HTTP_PORT}/result/initrd",
                    line,
                )
            # Обрабатываем строку initrd
            elif line.strip().startswith("initrd"):
                line = f"initrd http://{server_ip}:{HTTP_PORT}/result/initrd"
            processed_lines.append(line)
        content = "\n".join(processed_lines)
        logger.info(content)

        logger.info(
            f"Отправка netboot.ipxe для машины {machine_name} с подставленными значениями"
        )
        return Response(content=content, media_type="text/plain")

    except Exception as e:
        logger.error(f"Ошибка чтения файла netboot.ipxe: {e}")
        raise HTTPException(500, "Ошибка чтения файла netboot.ipxe")


@app.get("/result/{file_path:path}")
async def serve_result_file(file_path: str, request: Request):
    logger.info(f"request {file_path}")
    """Обслуживает *любой* файл из .pxe/result по HTTP"""
    # Безопасный путь (предотвращает выход за пределы RESULT_DIR)
    requested_path = Path(file_path)
    logger.info(requested_path)
    # Очищаем путь от .. и . для безопасности
    safe_path = RESULT_DIR / requested_path
    logger.info(safe_path)

    # Проверяем, что путь находится внутри RESULT_DIR
    if not str(safe_path).startswith(str(RESULT_DIR)):
        raise HTTPException(404, "File not found (path traversal attempt)")

    if not safe_path.exists():
        raise HTTPException(404, f"File not found: {file_path}")

    if safe_path.is_dir():
        raise HTTPException(400, "Cannot serve directories")

    logger.info(f"Отдаю файл: {safe_path}")

    # Определяем MIME-тип по расширению
    extension = safe_path.suffix.lower()
    if extension in [".img", ".iso", ".bz2", ".gz", ".xz", ".bin", ".efi", ".kpxe"]:
        media_type = "application/octet-stream"
    elif extension in [".txt", ".ipxe", ".pxe", ".cfg", ".conf"]:
        media_type = "text/plain"
    else:
        # Для остальных файлов пытаемся определить как бинарные или текстовые
        # Простой способ - отдать как бинарные, если не текст
        try:
            content = safe_path.read_text(encoding="utf-8")
            media_type = "text/plain"
        except UnicodeDecodeError:
            media_type = "application/octet-stream"
            content = safe_path.read_bytes()
            return Response(content, media_type=media_type)
        return Response(content, media_type=media_type)

    # Отдаём файл как текст или бинарные данные
    try:
        content = safe_path.read_text(encoding="utf-8")
        return Response(content, media_type=media_type)
    except UnicodeDecodeError:
        # Если файл не текстовый, отдаём как бинарные данные
        content = safe_path.read_bytes()
        return Response(content, media_type=media_type)


# ==============================
# Запуск
# ==============================
def main():
    global core_api, crd_api, server_ip, interface, private_key_path, public_key_path  # Добавляем

    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=8000)
    parser.add_argument("--no-dnsmasq", action="store_true")
    parser.add_argument("--interface", type=str, help="Сетевой интерфейс для dnsmasq")
    parser.add_argument("--dhcp-range", type=str, default="192.168.2.0,proxy", 
                       help="Диапазон DHCP (например: 192.168.1.0,proxy или 192.168.1.100,192.168.1.200)")
    args = parser.parse_args()
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

    # Генерация SSH-ключей (опционально)
    private_key_path, public_key_path = generate_ssh_keys_if_missing()

    # Проверка и сборка netboot-образа
    build_nixos_netboot_if_missing(public_key_path)

    # Сеть
    if args.interface:
        interface = args.interface
        # Используем существующую функцию для получения IP
        _, server_ip = get_primary_interface_and_ip()
        if not server_ip:
            logger.error(f"Не удалось определить IP для интерфейса {interface}")
            sys.exit(1)
    else:
        interface, server_ip = get_primary_interface_and_ip()
        if not interface:
            logger.error("Не удалось определить интерфейс")
            sys.exit(1)

    # Файлы
    ensure_ipxe_binaries()

    # dnsmasq
    if not args.no_dnsmasq:
        start_dnsmasq(interface, server_ip, args.dhcp_range)

    # HTTP-сервер
    signal.signal(signal.SIGINT, lambda s, f: sys.exit(0))
    logger.info(f"PXE-сервер запущен на http://{server_ip}:{args.port}")
    logger.info(
        f"Загрузка начинается с: http://{server_ip}:{args.port}/boot.ipxe?mac=XX:XX:XX:XX:XX:XX"
    )
    logger.info(f"Файл netboot.pxe будет читаться из: {RESULT_DIR / 'netboot.ipxe'}")
    logger.info(f"Сгенерированные SSH-ключи: {SSH_DIR}")
    uvicorn.run(app, host="0.0.0.0", port=args.port, log_level="warning")


if __name__ == "__main__":
    main()
