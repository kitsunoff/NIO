#!/usr/bin/env python3
"""
PXE server with Kubernetes machine registration and netboot.ipxe serving from .pxe/result
"""

import asyncio
import os
import sys
import signal
import logging
import subprocess
import threading
import argparse
import base64
from pathlib import Path
from typing import Optional
import shutil

import uvicorn
from fastapi import FastAPI, Request, Response, HTTPException
from kubernetes import client, config
import kubernetes
from kubernetes.client.rest import ApiException

# ==============================
# Configuration
# ==============================
BASE_DIR = Path.cwd() / ".pxe"
RESULT_DIR = BASE_DIR / "result"
RESULT_TEMP_DIR = BASE_DIR / "temp-result"
SSH_DIR = BASE_DIR / "ssh"
TFTP_ROOT = BASE_DIR / "tftp"
HTTP_PORT = 8000

# Logging configuration
logging.basicConfig(
    level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s"
)
logger = logging.getLogger("pxe-k8s")

# Global variables
dnsmasq_proc: Optional[subprocess.Popen] = None
GROUP, VERSION, PLURAL = "nio.homystack.com", "v1alpha1", "machines"
crd_api = None
REGISTERED_MACHINES = set()


# ==============================
# Utilities
# ==============================
def get_primary_interface_and_ip():
    """Simple detection of primary network interface and IP (Linux/macOS)"""
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

    logger.error("Failed to determine network interface")
    return None, None


def ensure_ipxe_binaries():
    """Downloads required iPXE binaries"""
    TFTP_ROOT.mkdir(exist_ok=True)
    files = {
        "undionly.kpxe": "https://boot.ipxe.org/undionly.kpxe  ",
        "ipxe.efi": "https://boot.ipxe.org/ipxe.efi  ",
    }
    for name, url in files.items():
        dst = TFTP_ROOT / name
        if not dst.exists():
            logger.info(f"Downloading {name}...")
            import urllib.request

            urllib.request.urlretrieve(url, dst)


def generate_ssh_keys_if_missing():
    """Generates SSH keys if they don't exist."""
    SSH_DIR.mkdir(exist_ok=True)
    private_key_path = SSH_DIR / "id_rsa"
    public_key_path = SSH_DIR / "id_rsa.pub"

    if private_key_path.exists() and public_key_path.exists():
        logger.info(f"SSH keys already exist: {private_key_path}, {public_key_path}")
        return str(private_key_path), str(public_key_path)

    logger.info("SSH keys not found, generating new ones...")
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
        logger.info(f"SSH keys generated: {private_key_path}, {public_key_path}")
        return str(private_key_path), str(public_key_path)
    except subprocess.CalledProcessError as e:
        logger.error(f"SSH key generation error: {e}")
        sys.exit(1)
    except FileNotFoundError:
        logger.error(
            "ssh-keygen command not found. Ensure OpenSSH is installed."
        )
        sys.exit(1)


def build_nixos_netboot_if_missing(public_key_path: str, arch: str = "x86_64", use_binfmt: bool = False):
    """Checks for netboot files and builds them if missing.
    Uses custom configuration with SSH key.
    Supports cross-compilation for different architectures using NixOS best practices.

    Args:
        public_key_path: Path to SSH public key
        arch: Target architecture (x86_64, aarch64, or armv7l)
    """
    # Map architecture to NixOS system string
    arch_map = {
        "x86_64": "x86_64-linux",
        "aarch64": "aarch64-linux",
        "armv7l": "armv7l-linux",
    }

    # Validate architecture
    if arch not in arch_map:
        logger.error(f"Unsupported architecture: {arch}. Supported: {', '.join(arch_map.keys())}")
        sys.exit(1)

    # Architecture-specific result directory
    arch_result_dir = RESULT_DIR / arch
    arch_result_dir.mkdir(parents=True, exist_ok=True)
    netboot_ipxe_path = arch_result_dir / "netboot.ipxe"

    required_files_exist = netboot_ipxe_path.exists()

    if required_files_exist:
        logger.info(f"Netboot files for {arch} already exist in .pxe/result/{arch}, build not required.")
        return

    logger.info(f"Netboot files for {arch} not found, starting build...")

    # Add Nix path to PATH
    nix_bin_path = "/nix/var/nix/profiles/default/bin"
    current_path = os.environ.get("PATH", "")
    if nix_bin_path not in current_path:
        os.environ["PATH"] = f"{nix_bin_path}:{current_path}"
        logger.info(f"Added Nix path to PATH: {nix_bin_path}")

    # Path to custom configuration
    custom_config_path = BASE_DIR / f"configuration-{arch}.nix"

    # Check if configuration file exists
    if not custom_config_path.exists():
        logger.info(
            f"Configuration file {custom_config_path} not found, creating default..."
        )
        # Read public key content
        try:
            public_key_content = Path(public_key_path).read_text().strip()
            if not public_key_content.startswith("ssh-"):
                logger.error(
                    f"File {public_key_path} doesn't contain valid SSH public key."
                )
                public_key_content = ""
        except Exception as e:
            logger.error(f"Error reading public key from {public_key_path}: {e}")
            public_key_content = ""

        # Default configuration content
        # Detect if we need cross-compilation
        import platform
        current_machine = platform.machine().lower()
        current_system_arch = "x86_64" if current_machine in ["x86_64", "amd64"] else \
                             "aarch64" if current_machine in ["aarch64", "arm64"] else \
                             "armv7l" if current_machine.startswith("armv7") else "unknown"

        # Set crossSystem for cross-compilation OR set hostPlatform for native
        system_config = ""
        if current_system_arch != arch:
            # Cross-compilation: set the target system
            target_system = arch_map.get(arch, f"{arch}-linux")
            system_config = f'\n  nixpkgs.crossSystem.system = "{target_system}";'
            logger.info(f"Configuration: Cross-compiling from {current_system_arch} to {arch}")
        else:
            # Native compilation: explicitly set hostPlatform
            target_system = arch_map.get(arch, f"{arch}-linux")
            system_config = f'\n  nixpkgs.hostPlatform.system = "{target_system}";'
            logger.info(f"Configuration: Native build for {arch}")

        config_content = f"""{{ modulesPath, ... }}: {{
  imports = [ (modulesPath + "/installer/netboot/netboot-minimal.nix") ];{system_config}

  # Set stateVersion to avoid warnings
  system.stateVersion = "24.11";

  # Allow unsupported systems (for cross-compilation from macOS)
  nixpkgs.config.allowUnsupportedSystem = true;

  services.openssh.enable = true;
  users.users.root.openssh.authorizedKeys.keys = [
    {f'"{public_key_content}"' if public_key_content else ''}
  ];
}}
"""
        custom_config_path.write_text(config_content)
        logger.info(f"Created default configuration file: {custom_config_path}")

    # Build netboot with configuration
    logger.info(f"Building netboot for {arch} with configuration: {custom_config_path}")

    nix_system = arch_map.get(arch)

    # Detect if we're cross-compiling
    import platform
    current_machine = platform.machine().lower()
    is_native_build = (
        (arch == "x86_64" and current_machine in ["x86_64", "amd64"]) or
        (arch == "aarch64" and current_machine in ["aarch64", "arm64"]) or
        (arch == "armv7l" and current_machine.startswith("armv7"))
    )

    if is_native_build:
        logger.info(f"Native build for {arch}")
        # Use standard netboot build for native compilation
        nix_expression = f"""
with import <nixpkgs/nixos/release.nix> {{ configuration = import {custom_config_path}; }};
netboot.{nix_system}
"""
    elif use_binfmt:
        logger.info(f"Using binfmt_misc emulation for {arch} (building on {current_machine})")
        # With binfmt, we can build as if it's native but emulated through QEMU
        # Nix will transparently use QEMU user-mode emulation
        nix_expression = f"""
with import <nixpkgs/nixos/release.nix> {{ configuration = import {custom_config_path}; }};
netboot.{nix_system}
"""
    else:
        logger.info(f"Cross-compiling for {arch} (building on {current_machine})")
        # For cross-compilation, the configuration already has nixpkgs.crossSystem set
        # Build the complete netboot package (ipxe script, kernel, initrd)
        nix_expression = f"""
let
  # Import pkgs with cross-compilation settings
  pkgs = import <nixpkgs> {{}};

  # Evaluate NixOS configuration with cross-compilation
  eval = import <nixpkgs/nixos> {{
    configuration = import {custom_config_path};
  }};

  # Build the complete netboot outputs
  kernel = eval.config.system.build.kernel;
  initrd = eval.config.system.build.netbootRamdisk;
  kernelTarget = eval.config.system.boot.loader.kernelFile;
  kernelParams = toString eval.config.boot.kernelParams;
  toplevel = eval.config.system.build.toplevel;

  # Create iPXE script
  ipxeScript = pkgs.writeText "netboot.ipxe" ''
    #!ipxe
    kernel bzImage init=${{toplevel}}/init initrd=initrd ${{kernelParams}}
    initrd initrd
    boot
  '';
in
  pkgs.runCommand "netboot" {{}} ''
    mkdir -p $out
    cp ${{ipxeScript}} $out/netboot.ipxe
    cp ${{kernel}}/${{kernelTarget}} $out/bzImage
    cp ${{initrd}}/initrd $out/initrd
  ''
"""

    arch_temp_dir = RESULT_TEMP_DIR / arch
    cmd = ["nix-build", "-E", nix_expression, "-o", str(arch_temp_dir)]

    # Set up environment for binfmt or cross-compilation
    build_env = os.environ.copy()

    # Allow building Linux systems on macOS/Darwin
    # The configuration already has nixpkgs.config.allowUnsupportedSystem = true
    # but we also set the environment variable as a backup
    if not is_native_build:
        build_env["NIXPKGS_ALLOW_UNSUPPORTED_SYSTEM"] = "1"
        logger.info("Cross-platform build enabled (macOS -> Linux)")

    if use_binfmt and not is_native_build:
        # Enable emulated systems in Nix (requires nix.conf: extra-platforms = aarch64-linux armv7l-linux)
        build_env["QEMU_LD_PREFIX"] = f"/run/binfmt/{nix_system}"
        logger.info(f"Set QEMU_LD_PREFIX for binfmt emulation")

    logger.info(f"Executing: nix-build -E '<expression>' -o {arch_temp_dir}")

    try:
        process = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
            universal_newlines=True,
            env=build_env,
        )

        if process.stdout:
            for line in process.stdout:
                logger.info(f"nix-build: {line.strip()}")

        process.wait()

        if process.returncode != 0:
            raise subprocess.CalledProcessError(process.returncode, cmd)

        # The nix-build creates a symlink at arch_temp_dir pointing to the nix store
        # We need to resolve it and copy the actual files
        build_result = Path(arch_temp_dir)

        if not build_result.exists():
            logger.error(f"Build result not found at {build_result}")
            sys.exit(1)

        # Resolve the symlink to get the actual nix store path
        nix_store_path = build_result.resolve()
        logger.info(f"Build completed, result at: {nix_store_path}")

        # List what's in the nix store result
        if nix_store_path.is_dir():
            logger.info(f"Contents of build result:")
            for item in nix_store_path.iterdir():
                logger.info(f"  - {item.name} ({'dir' if item.is_dir() else 'file'})")

            # Copy all files from nix store to our result directory
            for item in nix_store_path.iterdir():
                target = arch_result_dir / item.name
                if item.is_dir():
                    if target.exists():
                        shutil.rmtree(target)
                    shutil.copytree(item, target, symlinks=False)
                    logger.info(f"Copied directory {item.name}")
                else:
                    shutil.copy2(item, target)
                    logger.info(f"Copied file {item.name}")
        else:
            # If it's a single file (shouldn't happen for netboot, but handle it)
            logger.warning(f"Build result is a single file, not a directory: {nix_store_path}")

        # Verify the required files exist
        required_files = ["netboot.ipxe", "bzImage", "initrd"]
        missing_files = [f for f in required_files if not (arch_result_dir / f).exists()]

        if missing_files:
            logger.error(f"Missing required files after build: {', '.join(missing_files)}")
            logger.error(f"Files in {arch_result_dir}:")
            for item in arch_result_dir.iterdir():
                logger.error(f"  - {item.name}")
            sys.exit(1)

        logger.info(f"Netboot build for {arch} completed successfully.")
        logger.info(f"Files available: {', '.join([f.name for f in arch_result_dir.iterdir()])}")


    except subprocess.CalledProcessError as e:
        logger.error(f"Netboot build error: {e}")
        sys.exit(1)
    except FileNotFoundError:
        logger.error("nix-build command not found. Ensure Nix is installed.")
        sys.exit(1)
    except Exception as e:
        logger.error(f"Unexpected error during netboot build: {e}")
        sys.exit(1)


def generate_dnsmasq_conf(interface: str, server_ip: str, tftp_root: Path, dhcp_range: str) -> str:
    conf = f"""
interface={interface}
bind-interfaces

dhcp-range={dhcp_range}

# TFTP only for initial boot of non-iPXE clients
enable-tftp
tftp-root={tftp_root}

# Detect iPXE clients
dhcp-userclass=set:ipxe,iPXE
dhcp-match=set:ipxe,175,#iPXE

# For non-iPXE clients: load ipxe.efi via TFTP
dhcp-boot=tag:!ipxe,ipxe.efi

# For iPXE clients: force HTTP and block TFTP
dhcp-option=tag:ipxe,66,192.168.2.121
dhcp-option=tag:ipxe,67,http://{server_ip}:8000/boot.ipxe
dhcp-option=tag:ipxe,60,"iPXE"
pxe-service=tag:ipxe,X86-64_EFI,"iPXE",http://{server_ip}:8000/boot.ipxe

log-dhcp
"""
    # Save to .pxe/dnsmasq.conf (useful for debugging)
    conf_path = BASE_DIR / "dnsmasq.conf"
    conf_path.write_text(conf)
    return str(conf_path)


def start_dnsmasq(interface: str, server_ip: str, dhcp_range: str):
    """Starts dnsmasq in a separate process"""
    global dnsmasq_proc
    conf_path = generate_dnsmasq_conf(interface, server_ip, TFTP_ROOT, dhcp_range)
    cmd = ["dnsmasq", "--no-daemon", "--conf-file=" + conf_path, "--log-dhcp"]
    logger.info("Starting dnsmasq...")
    dnsmasq_proc = subprocess.Popen(
        cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT
    )

    def log_output():
        for line in iter(dnsmasq_proc.stdout.readline, b""):
            logger.info(f"dnsmasq: {line.decode().strip()}")

    threading.Thread(target=log_output, daemon=True).start()


def register_machine_in_k8s(mac: str, ip: str) -> str:
    """Registers machine in Kubernetes and creates Secret with SSH key."""
    mac_norm = mac.replace(":", "-").lower()
    machine_name = f"machine-{mac_norm}"
    secret_name = f"ssh-private-key-{mac_norm}"

    if mac_norm in REGISTERED_MACHINES:
        logger.info(f"Machine {machine_name} already registered in this session")
        return machine_name

    # 1. Read private key
    try:
        private_key_content = Path(private_key_path).read_text()
    except Exception as e:
        logger.error(f"Error reading private key {private_key_path}: {e}")
        raise HTTPException(500, "Failed to read private key")

    # 2. Create Secret
    secret_body = {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {
            "name": secret_name,
            "namespace": "default",
        },
        "type": "Opaque",
        "data": {
            # Keys in Secret must be base64 encoded
            "ssh-privatekey": base64.b64encode(private_key_content.encode()).decode()
        },
    }

    try:
        core_api.create_namespaced_secret("default", secret_body)
        logger.info(f"Secret {secret_name} created in K8s")
    except ApiException as e:
        if e.status != 409:  # 409 = already exists
            logger.error(f"Error creating Secret {secret_name}: {e}")
            raise HTTPException(500, "Secret creation failed")
        else:
            logger.info(f"Secret {secret_name} already exists in K8s")

    # 3. Create Machine object
    machine_body = {
        "apiVersion": f"{GROUP}/{VERSION}",
        "kind": "Machine",
        "metadata": {"name": machine_name, "namespace": "default"},
        "spec": {
            "hostname": ip,
            "sshUser": "root",  # Or pass as argument if needed
            "macAddress": mac,
            "sshKeySecretRef": {"name": secret_name, "namespace": "default"},
        },
    }

    try:
        crd_api.create_namespaced_custom_object(
            GROUP, VERSION, "default", PLURAL, machine_body
        )
        logger.info(f"Machine {machine_name} registered in K8s")
        REGISTERED_MACHINES.add(mac_norm)
    except ApiException as e:
        if e.status != 409:
            logger.error(f"K8s error creating Machine: {e}")
            # If Machine creation failed but Secret was created, we might want to delete Secret
            # For now, just throw error
            raise HTTPException(500, "Machine registration failed")
        else:
            logger.info(f"Machine {machine_name} already exists in K8s")
            REGISTERED_MACHINES.add(mac_norm)

    return machine_name


# ==============================
# HTTP Server
# ==============================
app = FastAPI(title="PXE + K8s Registrar")


@app.get("/boot.ipxe")
async def boot_script(request: Request, mac: Optional[str] = None, arch: Optional[str] = "x86_64"):
    """First iPXE script - registers machine and serves netboot.pxe"""

    script = f"""#!ipxe
dhcp
chain http://{server_ip}:{HTTP_PORT}/netboot.pxe?mac=${{mac}}&ip=${{ip}}&arch={arch}
"""
    return Response(content=script, media_type="text/plain")


@app.get("/netboot.pxe")
async def netboot_script(
    request: Request, mac: Optional[str] = None, ip: Optional[str] = None, arch: Optional[str] = "x86_64"
):
    """Serves netboot.ipxe file from .pxe/result with MAC and IP substitution"""
    logger.info(f"Request netboot.pxe from {request.client.host} with MAC {mac}, IP {ip}, arch {arch}")

    if not mac or not ip:
        raise HTTPException(400, "MAC and IP are required")

    machine_name = register_machine_in_k8s(mac, ip)

    # Architecture-specific netboot file
    arch_result_dir = RESULT_DIR / arch
    netboot_file_path = arch_result_dir / "netboot.ipxe"

    if not netboot_file_path.exists():
        logger.error(f"File netboot.ipxe not found: {netboot_file_path}")
        raise HTTPException(404, f"File netboot.ipxe not found in .pxe/result/{arch}")

    try:
        content = netboot_file_path.read_text()
        # Process iPXE script line by line and replace file references with HTTP URLs
        import re

        lines = content.splitlines()
        processed_lines = []

        for line in lines:
            # Process kernel line
            if line.strip().startswith("kernel"):
                # Replace kernel filename (bzImage) with full HTTP URL
                line = re.sub(
                    r'\bkernel\s+bzImage\b',
                    f'kernel http://{server_ip}:{HTTP_PORT}/result/{arch}/bzImage',
                    line
                )
                # Replace initrd=initrd parameter with full HTTP URL
                line = re.sub(
                    r'(\binitrd=)initrd\b',
                    rf'\g<1>http://{server_ip}:{HTTP_PORT}/result/{arch}/initrd',
                    line
                )
            # Process initrd line (separate command)
            elif line.strip().startswith("initrd"):
                line = f"initrd http://{server_ip}:{HTTP_PORT}/result/{arch}/initrd"

            processed_lines.append(line)

        content = "\n".join(processed_lines)
        logger.info(f"Processed iPXE script for {arch}:")
        logger.info(content)

        logger.info(
            f"Sending netboot.ipxe for machine {machine_name} with substituted values"
        )
        return Response(content=content, media_type="text/plain")

    except Exception as e:
        logger.error(f"Error reading netboot.ipxe file: {e}")
        raise HTTPException(500, "Error reading netboot.ipxe file")


@app.get("/result/{file_path:path}")
async def serve_result_file(file_path: str, request: Request):
    logger.info(f"request {file_path}")
    """Serves any file from .pxe/result via HTTP"""
    # Safe path (prevents path traversal outside RESULT_DIR)
    requested_path = Path(file_path)
    logger.info(requested_path)
    # Clean path from .. and . for security
    safe_path = RESULT_DIR / requested_path
    logger.info(safe_path)

    # Check that path is within RESULT_DIR
    if not str(safe_path).startswith(str(RESULT_DIR)):
        raise HTTPException(404, "File not found (path traversal attempt)")

    if not safe_path.exists():
        raise HTTPException(404, f"File not found: {file_path}")

    if safe_path.is_dir():
        raise HTTPException(400, "Cannot serve directories")

    logger.info(f"Serving file: {safe_path}")

    # Determine MIME type by extension
    extension = safe_path.suffix.lower()
    if extension in [".img", ".iso", ".bz2", ".gz", ".xz", ".bin", ".efi", ".kpxe"]:
        media_type = "application/octet-stream"
    elif extension in [".txt", ".ipxe", ".pxe", ".cfg", ".conf"]:
        media_type = "text/plain"
    else:
        # For other files, try to determine if binary or text
        # Simple approach - serve as binary if not text
        try:
            content = safe_path.read_text(encoding="utf-8")
            media_type = "text/plain"
        except UnicodeDecodeError:
            media_type = "application/octet-stream"
            content = safe_path.read_bytes()
            return Response(content, media_type=media_type)
        return Response(content, media_type=media_type)

    # Serve file as text or binary data
    try:
        content = safe_path.read_text(encoding="utf-8")
        return Response(content, media_type=media_type)
    except UnicodeDecodeError:
        # If file is not text, serve as binary data
        content = safe_path.read_bytes()
        return Response(content, media_type=media_type)


# ==============================
# Startup
# ==============================
def main():
    global core_api, crd_api, server_ip, interface, private_key_path, public_key_path

    BASE_DIR.mkdir(parents=True, exist_ok=True)
    RESULT_DIR.mkdir(parents=True, exist_ok=True)

    parser = argparse.ArgumentParser(description="PXE server with Kubernetes integration and multi-architecture support")
    parser.add_argument("--port", type=int, default=8000, help="HTTP server port")
    parser.add_argument("--no-dnsmasq", action="store_true", help="Don't start dnsmasq server")
    parser.add_argument("--interface", type=str, help="Network interface for dnsmasq")
    parser.add_argument("--dhcp-range", type=str, default="192.168.2.0,proxy",
                       help="DHCP range (e.g., 192.168.1.0,proxy or 192.168.1.100,192.168.1.200)")
    parser.add_argument("--architectures", type=str, default="x86_64",
                       help="Comma-separated list of architectures to build (x86_64,aarch64,armv7l)")
    parser.add_argument("--use-binfmt", action="store_true",
                       help="Use binfmt_misc emulation for cross-compilation (requires QEMU user-mode)")
    args = parser.parse_args()
    # Connect to Kubernetes
    try:
        kubernetes.config.load_kube_config()
        logger.info("Kubernetes config loaded from kubeconfig file.")
    except kubernetes.config.ConfigException:
        try:
            kubernetes.config.load_incluster_config()
            logger.info("Kubernetes config loaded from in-cluster environment.")
        except kubernetes.config.ConfigException as e:
            logger.error(f"K8s connection error: {e}")
            sys.exit(1)

    # Generate SSH keys (optional)
    private_key_path, public_key_path = generate_ssh_keys_if_missing()

    # Parse architectures
    architectures = [arch.strip() for arch in args.architectures.split(",")]
    logger.info(f"Building netboot images for architectures: {', '.join(architectures)}")

    # Check and build netboot images for each architecture
    for arch in architectures:
        build_nixos_netboot_if_missing(public_key_path, arch, args.use_binfmt)

    # Network configuration
    if args.interface:
        interface = args.interface
        # Use existing function to get IP
        _, server_ip = get_primary_interface_and_ip()
        if not server_ip:
            logger.error(f"Failed to determine IP for interface {interface}")
            sys.exit(1)
    else:
        interface, server_ip = get_primary_interface_and_ip()
        if not interface:
            logger.error("Failed to determine interface")
            sys.exit(1)

    # Ensure iPXE binaries
    ensure_ipxe_binaries()

    # Start dnsmasq
    if not args.no_dnsmasq:
        start_dnsmasq(interface, server_ip, args.dhcp_range)

    # Initialize Kubernetes APIs
    core_api = client.CoreV1Api()
    crd_api = client.CustomObjectsApi()

    # HTTP server
    signal.signal(signal.SIGINT, lambda _s, _f: sys.exit(0))
    logger.info(f"PXE server started at http://{server_ip}:{args.port}")
    logger.info(
        f"Boot starts from: http://{server_ip}:{args.port}/boot.ipxe?mac=XX:XX:XX:XX:XX:XX&arch=x86_64"
    )
    logger.info(f"Netboot files will be read from: {RESULT_DIR}/<arch>/netboot.ipxe")
    logger.info(f"Generated SSH keys: {SSH_DIR}")
    logger.info(f"Available architectures: {', '.join(architectures)}")
    uvicorn.run(app, host="0.0.0.0", port=args.port, log_level="warning")


if __name__ == "__main__":
    main()
