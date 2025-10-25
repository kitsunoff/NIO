#!/usr/bin/env python3

import os
import sys
import signal
import logging
import tempfile
import subprocess
import threading
import argparse
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
# Сеть (Cross-platform)
# ==============================
def get_primary_interface_and_ip():
    """
    Cross-platform function to get the primary network interface and IP address.
    Returns (interface_name, ip_address)
    """
    
    # Method 1: macOS - use ipconfig (most reliable on macOS)
    try:
        # Try common macOS interfaces
        for interface in ["en0", "en1", "en2"]:
            try:
                ip = subprocess.check_output(["ipconfig", "getifaddr", interface], text=True).strip()
                if ip and ip != "":
                    logger.info(f"Определен интерфейс {interface} с IP {ip} через ipconfig (macOS)")
                    return interface, ip
            except subprocess.CalledProcessError:
                continue
    except FileNotFoundError:
        logger.warning("ipconfig не найден, пробуем другие методы...")
    except Exception as e:
        logger.warning(f"Ошибка ipconfig: {e}, пробуем другие методы...")
    
    # Method 2: Linux - use iproute2 (ip command)
    try:
        # Get primary interface through ip route
        route_output = subprocess.check_output(["ip", "route", "get", "1"], text=True)
        logger.info(f"ip route output: {route_output}")
        # Example output: 1.1.1.1 via 172.17.0.1 dev eth0 src 172.17.0.2 uid 0
        parts = route_output.strip().split()
        iface = parts[parts.index("dev") + 1]
        logger.info(f"Found interface: {iface}")
        
        # Get IP address of the interface
        addr_output = subprocess.check_output(["ip", "addr", "show", iface], text=True)
        for line in addr_output.splitlines():
            if "inet " in line:
                ip = line.strip().split()[1].split('/')[0]
                logger.info(f"Определен интерфейс {iface} с IP {ip} через iproute2 (Linux)")
                return iface, ip
        
        raise Exception(f"Не удалось найти IP для интерфейса {iface}")
    except FileNotFoundError:
        logger.warning("iproute2 не найден, пробуем другие методы...")
    except Exception as e:
        logger.warning(f"Ошибка iproute2: {e}, пробуем другие методы...")
    
    # Method 3: Cross-platform - use ifconfig
    try:
        # Try common interface names
        interface_candidates = ["eth0", "ens33", "ens192", "enp0s3", "wlan0", "en0", "en1", "en2"]
        for iface in interface_candidates:
            try:
                ifconfig_output = subprocess.check_output(["ifconfig", iface], text=True)
                for line in ifconfig_output.splitlines():
                    if "inet " in line and "127.0.0.1" not in line:
                        parts = line.strip().split()
                        ip = parts[1]
                        logger.info(f"Определен интерфейс {iface} с IP {ip} через ifconfig")
                        return iface, ip
            except (subprocess.CalledProcessError, FileNotFoundError):
                continue
    except Exception as e:
        logger.warning(f"Ошибка ifconfig: {e}")
    
    # Method 4: Linux - use hostname
    try:
        hostname_output = subprocess.check_output(["hostname", "-I"], text=True)
        if hostname_output.strip():
            ip = hostname_output.strip().split()[0]
            # Try to find the interface for this IP
            try:
                route_output = subprocess.check_output(["ip", "route", "get", ip], text=True)
                parts = route_output.strip().split()
                iface = parts[parts.index("dev") + 1]
                logger.info(f"Определен интерфейс {iface} с IP {ip} через hostname")
                return iface, ip
            except:
                logger.info(f"Определен IP {ip} через hostname (интерфейс неизвестен)")
                return "unknown", ip
    except Exception as e:
        logger.warning(f"Ошибка hostname: {e}")
    
    # Method 5: macOS - use networksetup as fallback
    try:
        service_output = subprocess.check_output(["networksetup", "-listallnetworkservices"], text=True)
        for line in service_output.splitlines():
            if line.strip() and not line.startswith("An asterisk"):
                service = line.strip()
                try:
                    ip_output = subprocess.check_output(["networksetup", "-getinfo", service], text=True)
                    for ip_line in ip_output.splitlines():
                        if "IP address:" in ip_line:
                            ip = ip_line.split(":")[1].strip()
                            if ip and ip != "none":
                                logger.info(f"Определен интерфейс {service} с IP {ip} через networksetup (macOS)")
                                return service, ip
                except:
                    continue
    except Exception as e:
        logger.warning(f"Ошибка networksetup: {e}")
    
    # Fallback: Use localhost
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
interface={interface}
bind-interfaces

dhcp-range=192.168.2.0,proxy

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
        if dnsmasq_proc and dnsmasq_proc.stdout:
            for line in iter(dnsmasq_proc.stdout.readline, b""):
                logger.info(f"dnsmasq: {line.decode().strip()}")

    threading.Thread(target=log_output, daemon=True).start()


# ==============================
# Machine Registration
# ==============================
def register_machine_in_k8s(mac: str, ip: str) -> str:
    """Register machine in Kubernetes by MAC address and return machine name"""
    mac_norm = mac.replace(":", "-").replace(".", "-").lower()
    machine_name = f"machine-{mac_norm}"

    if mac_norm not in REGISTERED_MACHINES:
        logger.info(f"Регистрация машины {machine_name} с IP {ip}")
        body = {
            "apiVersion": f"{GROUP}/{VERSION}",
            "kind": "Machine",
            "metadata": {"name": machine_name, "namespace": "default"},
            "spec": {"hostname": ip, "sshUser": "root", "macAddress": mac}
        }
        try:
            crd_api.create_namespaced_custom_object(GROUP, VERSION, "default", PLURAL, body)
            REGISTERED_MACHINES.add(mac_norm)
        except ApiException as e:
            if e.status != 409:
                logger.error(f"K8s error: {e}")
                raise HTTPException(500, "Registration failed")
            REGISTERED_MACHINES.add(mac_norm)
    
    return machine_name


# ==============================
# Generic File Serving
# ==============================
def get_file_mime_type(file_path: Path) -> str:
    """Determine MIME type based on file extension"""
    extension = file_path.suffix.lower()
    mime_types = {
        '.ipxe': 'text/plain',
        '.pxe': 'text/plain',
        '.txt': 'text/plain',
        '.conf': 'text/plain',
        '.sh': 'text/plain',
        '.py': 'text/plain',
        '.kernel': 'application/octet-stream',
        '.initrd': 'application/octet-stream',
        '.efi': 'application/octet-stream',
        '.kpxe': 'application/octet-stream',
        '.iso': 'application/octet-stream',
        '.img': 'application/octet-stream',
    }
    return mime_types.get(extension, 'application/octet-stream')


# ==============================
# FastAPI Endpoints
# ==============================
app = FastAPI(title="Unified PXE + K8s Registrar")


@app.get("/{file_path:path}")
async def serve_file(file_path: str, request: Request):
    """Serve any file from the .pxe directory"""
    # Security: prevent directory traversal
    safe_path = Path(file_path).resolve()
    base_path = BASE_DIR.resolve()
    
    if not str(safe_path).startswith(str(base_path)):
        # If not in base directory, try to find it in BASE_DIR
        safe_path = base_path / file_path
    
    # Ensure the file is within BASE_DIR
    safe_path = safe_path.resolve()
    if not str(safe_path).startswith(str(base_path)):
        raise HTTPException(404, "File not found")
    
    if not safe_path.exists():
        raise HTTPException(404, f"File not found: {file_path}")
    
    if safe_path.is_dir():
        raise HTTPException(400, "Cannot serve directories")
    
    logger.info(f"Serving file: {safe_path}")
    
    # Determine MIME type
    mime_type = get_file_mime_type(safe_path)
    
    # Read and serve file
    if mime_type.startswith('text/'):
        return Response(safe_path.read_text(), media_type=mime_type)
    else:
        return Response(safe_path.read_bytes(), media_type=mime_type)


@app.get("/boot.ipxe")
async def boot_script(request: Request, mac: Optional[str] = None):
    """Initial iPXE boot script - registers machine and chains to netboot"""
    logger.info(f"Boot request from {request.client.host if request.client else 'unknown'} (MAC: {mac})")
    
    if request.client is None:
        raise HTTPException(400, "Client IP not available")
    
    ip = request.client.host
    try:
        ip_address(ip)
    except ValueError:
        raise HTTPException(400, "Invalid client IP")

    # Register machine if MAC is provided
    machine_name = "unknown"
    if mac:
        machine_name = register_machine_in_k8s(mac, ip)

    script = f"""#!ipxe
echo Booting machine {machine_name}
dhcp
chain http://{ip}:{HTTP_PORT}/result/netboot.pxe?mac={{mac}}&ip={{ip}}
"""
    return Response(script, media_type="text/plain")


@app.get("/result/netboot.pxe")
async def netboot_script(request: Request, mac: Optional[str] = None, ip: Optional[str] = None):
    """Serve netboot.pxe file from .pxe directory"""
    logger.info(f"Netboot request from {request.client.host if request.client else 'unknown'} (MAC: {mac})")
    
    if request.client is None:
        raise HTTPException(400, "Client IP not available")
    
    if ip is None:
        raise HTTPException(400, "Client IP not available")

        
    # Register machine if MAC is provided
    machine_name = "unknown"
    if mac:
        machine_name = register_machine_in_k8s(mac, ip)

    # Serve the actual netboot.pxe file from .pxe directory
    netboot_path = BASE_DIR / "netboot.pxe"
    if netboot_path.exists():
        logger.info(f"Serving netboot.pxe from: {netboot_path}")
        return Response(netboot_path.read_text(), media_type="text/plain")
    else:
        # If file doesn't exist, return 404
        raise HTTPException(404, "netboot.pxe file not found in .pxe directory")


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
# Netboot Image Building
# ==============================
def build_nixos_netboot(output_dir: Path):
    """Build NixOS netboot image in the .pxe directory"""
    logger.info("Building NixOS netboot image...")
    
    try:
        # Create result directory if it doesn't exist
        result_dir = output_dir / "result"
        result_dir.mkdir(exist_ok=True)
        
        # Check if we have a flake.nix in the .pxe directory
        flake_path = output_dir / "flake.nix"
        if flake_path.exists():
            logger.info("Found flake.nix, building custom netboot image...")
            # Build from local flake
            process = subprocess.Popen(
                ["nix", "build", f"{output_dir}#netboot", "-o", str(result_dir)],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1,
                universal_newlines=True
            )
        else:
            logger.info("No flake.nix found, building standard netboot image...")
            # Build standard netboot from nixpkgs
            process = subprocess.Popen(
                ["nix-build", "-A", "netboot.x86_64-linux", "<nixpkgs/nixos/release.nix>", "-o", str(result_dir)],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1,
                universal_newlines=True
            )
        
        # Stream output in real-time
        if process.stdout:
            for line in process.stdout:
                logger.info(f"nix-build: {line.strip()}")
        
        # Wait for process to complete
        process.wait()
        
        if process.returncode != 0:
            raise subprocess.CalledProcessError(process.returncode, process.args)
        
        # Find and copy the built files
        copy_netboot_files(result_dir, output_dir)
        
        logger.info("NixOS netboot image built successfully")
        
    except subprocess.CalledProcessError as e:
        logger.error(f"Failed to build NixOS netboot image: {e}")
        raise
    except Exception as e:
        logger.error(f"Error building NixOS netboot image: {e}")
        raise


def copy_netboot_files(result_dir: Path, output_dir: Path):
    """Copy kernel, initrd, and netboot.ipxe from build result"""
    # Look for kernel, initrd, and netboot.ipxe in the result
    kernel_path = None
    initrd_path = None
    netboot_ipxe_path = None
    
    for item in result_dir.rglob("*"):
        if "bzImage" in item.name or "vmlinuz" in item.name:
            kernel_path = item
        elif "initrd" in item.name:
            initrd_path = item
        elif "netboot.ipxe" in item.name:
            netboot_ipxe_path = item
    
    # Copy kernel
    if kernel_path and kernel_path.exists():
        LOCAL_KERNEL_PATH.write_bytes(kernel_path.read_bytes())
        logger.info(f"Copied kernel to {LOCAL_KERNEL_PATH}")
    
    # Copy initrd
    if initrd_path and initrd_path.exists():
        LOCAL_INITRD_PATH.write_bytes(initrd_path.read_bytes())
        logger.info(f"Copied initrd to {LOCAL_INITRD_PATH}")
    
    # Copy netboot.ipxe if it exists
    if netboot_ipxe_path and netboot_ipxe_path.exists():
        netboot_dest = output_dir / "netboot.ipxe"
        netboot_dest.write_text(netboot_ipxe_path.read_text())
        logger.info(f"Copied netboot.ipxe to {netboot_dest}")
    
    # Create a basic netboot.ipxe if it doesn't exist
    netboot_ipxe_path = output_dir / "netboot.ipxe"
    if not netboot_ipxe_path.exists():
        create_default_netboot_ipxe(netboot_ipxe_path)


def create_default_netboot_ipxe(output_path: Path):
    """Create a default netboot.ipxe script"""
    script = """#!ipxe
echo Starting NixOS netboot...
echo Loading kernel and initrd...

# Use relative paths for files in the same directory
kernel nixos-kernel init=/init console=ttyS0,115200n8
initrd nixos-initrd

echo Booting NixOS...
boot
"""
    output_path.write_text(script)
    logger.info(f"Created default netboot.ipxe at {output_path}")


def build_nixos_image(nix_file: str, output_dir: Path):
    """Build NixOS image from a .nix file"""
    logger.info(f"Building NixOS image from {nix_file}")
    
    try:
        # Create result directory if it doesn't exist
        result_dir = output_dir / "result"
        result_dir.mkdir(exist_ok=True)
        
        # Build the image
        process = subprocess.Popen(
            ["nix-build", nix_file, "-o", str(result_dir)],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
            universal_newlines=True
        )
        
        # Stream output in real-time
        if process.stdout:
            for line in process.stdout:
                logger.info(f"nix-build: {line.strip()}")
        
        # Wait for process to complete
        process.wait()
        
        if process.returncode != 0:
            raise subprocess.CalledProcessError(process.returncode, process.args)
        
        # Find and copy the built files
        copy_netboot_files(result_dir, output_dir)
        
        logger.info("NixOS image built successfully")
        
    except subprocess.CalledProcessError as e:
        logger.error(f"Failed to build NixOS image: {e}")
        raise
    except Exception as e:
        logger.error(f"Error building NixOS image: {e}")
        raise


# ==============================
# Argument Parsing
# ==============================
def parse_arguments():
    """Parse command line arguments"""
    parser = argparse.ArgumentParser(description="PXE + Kubernetes Machine Registrar")
    parser.add_argument("--port", type=int, default=8000, help="HTTP server port (default: 8000)")
    parser.add_argument("--interface", type=str, help="Network interface to use (auto-detected if not specified)")
    parser.add_argument("--ip", type=str, help="Server IP address (auto-detected if not specified)")
    parser.add_argument("--kernel-url", type=str, help="NixOS kernel URL (overrides NIXOS_KERNEL_URL env)")
    parser.add_argument("--initrd-url", type=str, help="NixOS initrd URL (overrides NIXOS_INITRD_URL env)")
    parser.add_argument("--build-image", type=str, help="Build NixOS image from .nix file before starting server")
    parser.add_argument("--build-netboot", action="store_true", help="Build NixOS netboot image from nixpkgs/nixos/release.nix")
    parser.add_argument("--no-dnsmasq", action="store_true", help="Skip dnsmasq startup")
    parser.add_argument("--debug", action="store_true", help="Enable debug logging")
    
    return parser.parse_args()


# ==============================
# Запуск
# ==============================
def main():
    global crd_api, HTTP_PORT
    
    # Parse command line arguments
    args = parse_arguments()
    
    # Apply arguments
    HTTP_PORT = args.port
    if args.kernel_url:
        global NIXOS_KERNEL_URL
        NIXOS_KERNEL_URL = args.kernel_url
    if args.initrd_url:
        global NIXOS_INITRD_URL
        NIXOS_INITRD_URL = args.initrd_url
    
    # Configure logging
    if args.debug:
        logging.getLogger().setLevel(logging.DEBUG)
    
    # Build image if requested
    if args.build_netboot or args.build_image:
        try:
            if args.build_netboot:
                build_nixos_netboot(BASE_DIR)
            elif args.build_image:
                build_nixos_image(args.build_image, BASE_DIR)
        except Exception as e:
            logger.error(f"Failed to build image: {e}")
            if not args.no_dnsmasq:
                logger.info("Continuing with existing files...")
            else:
                sys.exit(1)
    
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
    if not args.no_dnsmasq:
        start_dnsmasq(interface, server_ip)

    # Инструкции
    logger.info("✅ PXE + K8s контроллер запущен!")
    logger.info(f"   Рабочая папка: {BASE_DIR}")
    logger.info(f"   Файловый сервер: http://{server_ip}:{HTTP_PORT}/")
    if NIXOS_KERNEL_URL:
        logger.info(f"   Ядро: {NIXOS_KERNEL_URL}")
        logger.info(f"   Initrd: {NIXOS_INITRD_URL or 'локальный'}")
    else:
        logger.info(f"   Образы: {LOCAL_KERNEL_PATH}, {LOCAL_INITRD_PATH}")
    logger.info(f"   iPXE: http://{server_ip}:{HTTP_PORT}/boot.ipxe?mac=aa:bb:cc:dd:ee:ff")
    logger.info(f"   Любой файл: http://{server_ip}:{HTTP_PORT}/filename")

    # HTTP-сервер
    uvicorn.run(app, host="0.0.0.0", port=HTTP_PORT, log_level="warning")


if __name__ == "__main__":
    main()
