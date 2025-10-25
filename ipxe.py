#!/usr/bin/env python3

import os
import sys
import signal
import logging
import tempfile
import subprocess
import threading
from pathlib import Path
from ipaddress import ip_address
from typing import Optional

import uvicorn
from fastapi import FastAPI, Request, Response, HTTPException
from kubernetes import client, config
from kubernetes.client.rest import ApiException

# ==============================
# Настройки: всё в .pxe внутри текущей папки
# ==============================
BASE_DIR = Path.cwd() / ".pxe"
BASE_DIR.mkdir(exist_ok=True)

TFTP_ROOT = BASE_DIR / "tftp"
TFTP_ROOT.mkdir(exist_ok=True)

# Внешние URL (опционально, через env)
NIXOS_KERNEL_URL = os.getenv("NIXOS_KERNEL_URL")
NIXOS_INITRD_URL = os.getenv("NIXOS_INITRD_URL")

# Локальные пути
LOCAL_KERNEL_PATH = BASE_DIR / "nixos-kernel"
LOCAL_INITRD_PATH = BASE_DIR / "nixos-initrd"

HTTP_PORT = 8000

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("pxe-k8s")

# Глобальные
dnsmasq_proc: Optional[subprocess.Popen] = None
REGISTERED_MACHINES = set()
GROUP, VERSION, PLURAL = "nixos.infra", "v1alpha1", "machines"


# ==============================
# Сеть (Linux в Docker)
# ==============================
def get_primary_interface_and_ip():
    # Метод 1: Используем iproute2 (ip команда) - Linux
    try:
        # Получаем основной интерфейс через ip route
        route_output = subprocess.check_output(["ip", "route", "get", "1"], text=True)
        logger.info(f"ip route output: {route_output}")
        # Пример вывода: 1.1.1.1 via 172.17.0.1 dev eth0 src 172.17.0.2 uid 0
        parts = route_output.strip().split()
        iface = parts[parts.index("dev") + 1]
        logger.info(f"Found interface: {iface}")
        
        # Получаем IP адрес интерфейса
        addr_output = subprocess.check_output(["ip", "addr", "show", iface], text=True)
        # Ищем строку с inet
        for line in addr_output.splitlines():
            if "inet " in line:
                ip = line.strip().split()[1].split('/')[0]
                logger.info(f"Определен интерфейс {iface} с IP {ip} через iproute2")
                return iface, ip
        
        raise Exception(f"Не удалось найти IP для интерфейса {iface}")
    except FileNotFoundError:
        logger.warning("iproute2 не найден, пробуем альтернативные методы...")
    except Exception as e:
        logger.warning(f"Ошибка iproute2: {e}, пробуем альтернативные методы...")
    
    # Метод 2: Используем netstat (если доступен) - Linux/macOS
    try:
        netstat_output = subprocess.check_output(["netstat", "-rn"], text=True)
        for line in netstat_output.splitlines():
            if "0.0.0.0" in line and "UG" in line:
                parts = line.split()
                iface = parts[-1]
                # Получаем IP через ifconfig или ipconfig
                try:
                    ifconfig_output = subprocess.check_output(["ifconfig", iface], text=True)
                    for ifconfig_line in ifconfig_output.splitlines():
                        if "inet " in ifconfig_line:
                            ip = ifconfig_line.strip().split()[1]
                            logger.info(f"Определен интерфейс {iface} с IP {ip} через netstat/ifconfig")
                            return iface, ip
                except FileNotFoundError:
                    pass
    except Exception as e:
        logger.warning(f"Ошибка netstat: {e}")
    
    # Метод 3: macOS-specific - используем route и ifconfig
    try:
        # Получаем основной маршрут через route
        route_output = subprocess.check_output(["route", "-n", "get", "default"], text=True)
        iface = None
        ip = None
        for line in route_output.splitlines():
            if "interface:" in line:
                iface = line.split(":")[1].strip()
            if "gateway:" in line:
                ip = line.split(":")[1].strip()
        
        if iface and ip:
            logger.info(f"Определен интерфейс {iface} с IP {ip} через route (macOS)")
            return iface, ip
    except Exception as e:
        logger.warning(f"Ошибка macOS route: {e}")
    
    # Метод 4: macOS - используем networksetup
    try:
        # Получаем активный сетевой сервис
        service_output = subprocess.check_output(["networksetup", "-listallnetworkservices"], text=True)
        for line in service_output.splitlines():
            if line.strip() and not line.startswith("An asterisk"):
                service = line.strip()
                try:
                    # Получаем IP для этого сервиса
                    ip_output = subprocess.check_output(["networksetup", "-getinfo", service], text=True)
                    for ip_line in ip_output.splitlines():
                        if "IP address:" in ip_line:
                            ip = ip_line.split(":")[1].strip()
                            if ip and ip != "none":
                                logger.info(f"Определен интерфейс {service} с IP {ip} через networksetup (macOS)")
                                return service, ip
                except:
                    pass
    except Exception as e:
        logger.warning(f"Ошибка networksetup: {e}")
    
    # Метод 5: Используем переменные окружения или эвристики
    # Пробуем определить через env переменные или стандартные интерфейсы
    interface_candidates = ["eth0", "ens33", "ens192", "enp0s3", "wlan0", "en0", "en1", "en2"]
    for iface in interface_candidates:
        try:
            # Проверяем существование интерфейса через /sys/class/net (Linux) или ifconfig (macOS)
            if Path(f"/sys/class/net/{iface}").exists():
                # Пробуем получить IP через hostname -I
                try:
                    hostname_output = subprocess.check_output(["hostname", "-I"], text=True)
                    ip = hostname_output.strip().split()[0]
                    logger.info(f"Определен интерфейс {iface} с IP {ip} через hostname")
                    return iface, ip
                except:
                    pass
        except:
            pass
    
    # Метод 6: Используем localhost как запасной вариант
    logger.warning("Не удалось определить сетевой интерфейс, использую localhost")
    return "lo", "127.0.0.1"


# ==============================
# Файлы
# ==============================
def ensure_ipxe_binaries():
    files = {
        "undionly.kpxe": "https://boot.ipxe.org/undionly.kpxe",
        "ipxe.efi": "https://boot.ipxe.org/ipxe.efi",
    }
    for name, url in files.items():
        dst = TFTP_ROOT / name
        if not dst.exists():
            logger.info(f"Скачиваю {name}...")
            import urllib.request
            urllib.request.urlretrieve(url, dst)


def ensure_nixos_placeholders():
    if not NIXOS_KERNEL_URL and not LOCAL_KERNEL_PATH.exists():
        logger.warning("NIXOS_KERNEL_URL не задан — создаю заглушку nixos-kernel")
        LOCAL_KERNEL_PATH.write_bytes(b"")
    if not NIXOS_INITRD_URL and not LOCAL_INITRD_PATH.exists():
        logger.warning("NIXOS_INITRD_URL не задан — создаю заглушку nixos-initrd")
        LOCAL_INITRD_PATH.write_bytes(b"")


# ==============================
# dnsmasq
# ==============================
def generate_dnsmasq_conf(interface: str, server_ip: str, tftp_root: Path) -> str:
    conf= f"""
interface=eth0
bind-interfaces

dhcp-range=192.168.2.0,proxy

# TFTP только для начальной загрузки НЕ-iPXE клиентов
enable-tftp
tftp-root=/home/kitsunoff/NIO/.pxe/tftp

# Определяем iPXE клиентов
dhcp-userclass=set:ipxe,iPXE
dhcp-match=set:ipxe,175,#iPXE

# Для НЕ-iPXE клиентов: загружаем ipxe.efi по TFTP
dhcp-boot=tag:!ipxe,ipxe.efi

# Для iPXE клиентов: принудительно используем HTTP и БЛОКИРУЕМ TFTP
dhcp-option=tag:ipxe,66,192.168.2.121
dhcp-option=tag:ipxe,67,http://192.168.2.121:8000/boot.ipxe
dhcp-option=tag:ipxe,60,"iPXE"
pxe-service=tag:ipxe,X86-64_EFI,"iPXE",http://192.168.2.121:8000/boot.ipxe

log-dhcp
"""
    # Сохраняем в .pxe/dnsmasq.conf (удобно для отладки)
    conf_path = BASE_DIR / "dnsmasq.conf"
    conf_path.write_text(conf)
    return str(conf_path)


def start_dnsmasq(interface: str, server_ip: str):
    global dnsmasq_proc
    conf_path = generate_dnsmasq_conf(interface, server_ip, TFTP_ROOT)
    cmd = ["dnsmasq", "--no-daemon", "--conf-file=" + conf_path, "--log-dhcp"]
    logger.info("Запуск dnsmasq (proxy DHCP + TFTP)...")
    dnsmasq_proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        preexec_fn=os.setsid
    )

    def log_output():
        if dnsmasq_proc.stdout:
            for line in iter(dnsmasq_proc.stdout.readline, b""):
                logger.info(f"dnsmasq: {line.decode().strip()}")

    threading.Thread(target=log_output, daemon=True).start()


# ==============================
# FastAPI
# ==============================
app = FastAPI(title="Unified PXE + K8s Registrar")


@app.get("/boot.ipxe")
async def boot_script(request: Request, mac: Optional[str] = None):
    logger.info("req arrived")
    if request.client is None:
        raise HTTPException(400, "Client IP not available")
    
    ip = request.client.host
    try:
        ip_address(ip)
    except ValueError:
        raise HTTPException(400, "Invalid client IP")

    if mac:
        mac_norm = mac.replace(":", "-").replace(".", "-").lower()
        machine_name = f"machine-{mac_norm}"

        if mac_norm not in REGISTERED_MACHINES:
            logger.info(f"Регистрация машины {machine_name} с IP {ip}")
            body = {
                "apiVersion": f"{GROUP}/{VERSION}",
                "kind": "Machine",
                "metadata": {"name": machine_name, "namespace": "default"},
                "spec": {"hostname": ip, "sshUser": "root"}
            }
            try:
                crd_api.create_namespaced_custom_object(GROUP, VERSION, "default", PLURAL, body)
                REGISTERED_MACHINES.add(mac_norm)
            except ApiException as e:
                if e.status != 409:
                    logger.error(f"K8s error: {e}")
                    raise HTTPException(500, "Registration failed")
                REGISTERED_MACHINES.add(mac_norm)
    else:
        machine_name = "unknown"

    script = f"""#!ipxe
dhcp
chain http://{ip}:{HTTP_PORT}/result/netboot.ipxe
"""
    return Response(script, media_type="text/plain")


@app.get("/main.ipxe")
async def main_script(request: Request):
    if request.client is None:
        raise HTTPException(400, "Client IP not available")
    
    ip = request.client.host
    kernel = NIXOS_KERNEL_URL or f"http://{ip}:{HTTP_PORT}/nixos-kernel"
    initrd = NIXOS_INITRD_URL or f"http://{ip}:{HTTP_PORT}/nixos-initrd"
    script = f"""#!ipxe
echo Загрузка NixOS через Kubernetes-управляемый PXE...
kernel {kernel} init=/init
initrd {initrd}
boot
"""
    return Response(script, media_type="text/plain")


# Эндпоинты для локальных файлов (только если URL не заданы)
if not NIXOS_KERNEL_URL:
    @app.get("/nixos-kernel")
    async def serve_kernel():
        if not LOCAL_KERNEL_PATH.exists():
            raise HTTPException(404, "nixos-kernel not found")
        return Response(LOCAL_KERNEL_PATH.read_bytes(), media_type="application/octet-stream")

if not NIXOS_INITRD_URL:
    @app.get("/nixos-initrd")
    async def serve_initrd():
        if not LOCAL_INITRD_PATH.exists():
            raise HTTPException(404, "nixos-initrd not found")
        return Response(LOCAL_INITRD_PATH.read_bytes(), media_type="application/octet-stream")


# ==============================
# Завершение
# ==============================
def cleanup(signum=None, frame=None):
    logger.info("Остановка...")
    if dnsmasq_proc:
        dnsmasq_proc.terminate()
        try:
            dnsmasq_proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            dnsmasq_proc.kill()
    sys.exit(0)


# ==============================
# Запуск
# ==============================
def main():
    global crd_api
    signal.signal(signal.SIGINT, cleanup)
    signal.signal(signal.SIGTERM, cleanup)

    # Kubernetes
    try:
        config.load_kube_config()
        crd_api = client.CustomObjectsApi()
    except Exception as e:
        logger.error(f"Ошибка kubeconfig: {e}")
        sys.exit(1)

    # Сеть
    interface, server_ip = get_primary_interface_and_ip()
    logger.info(f"Сервер: {server_ip} на интерфейсе {interface}")

    # Файлы
    ensure_ipxe_binaries()
    ensure_nixos_placeholders()

    # dnsmasq
    start_dnsmasq(interface, server_ip)

    # Инструкции
    logger.info("✅ PXE + K8s контроллер запущен!")
    logger.info(f"   Рабочая папка: {BASE_DIR}")
    if NIXOS_KERNEL_URL:
        logger.info(f"   Ядро: {NIXOS_KERNEL_URL}")
        logger.info(f"   Initrd: {NIXOS_INITRD_URL or 'локальный'}")
    else:
        logger.info(f"   Образы: {LOCAL_KERNEL_PATH}, {LOCAL_INITRD_PATH}")
    logger.info(f"   iPXE: http://{server_ip}:{HTTP_PORT}/boot.ipxe?mac=aa:bb:cc:dd:ee:ff")

    # HTTP-сервер
    uvicorn.run(app, host="0.0.0.0", port=HTTP_PORT, log_level="warning")


if __name__ == "__main__":
    main()
