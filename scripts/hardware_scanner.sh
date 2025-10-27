#!/usr/bin/env bash

# Helper function for safe file reading
safe_read() {
    local file="$1"
    if [ -r "$file" ]; then
        cat "$file" 2>/dev/null
    fi
}

# --- 1. OS and basic info ---
os_name="unknown"
os_id="unknown"
if [ -f /etc/os-release ]; then
    . /etc/os-release
    os_name="${NAME:-unknown}"
    os_id="${ID:-unknown}"
elif [ -f /etc/system-release ]; then
    # Amazon Linux, RHEL
    os_name="$(cat /etc/system-release 2>/dev/null)"
    if [[ "$os_name" == *"Amazon Linux"* ]]; then
        os_id="amzn"
    else
        os_id="rhel"
    fi
elif [ -f /etc/redhat-release ]; then
    os_name="$(cat /etc/redhat-release 2>/dev/null)"
    os_id="rhel"
elif [ -f /etc/debian_version ]; then
    os_name="Debian $(cat /etc/debian_version 2>/dev/null)"
    os_id="debian"
fi

kernel_version=$(uname -r 2>/dev/null || echo "unknown")
architecture=$(uname -m 2>/dev/null || echo "unknown")
hostname=$(hostname 2>/dev/null || echo "unknown")
uptime_days="unknown"
if [ -f /proc/uptime ]; then
    uptime_sec=$(cut -d. -f1 /proc/uptime 2>/dev/null)
    if [ -n "$uptime_sec" ] && [ "$uptime_sec" -ge 0 ] 2>/dev/null; then
        uptime_days=$((uptime_sec / 86400))
    fi
fi

# --- 2. CPU ---
cpu_model="unknown"
cpu_cores="unknown"
if [ -f /proc/cpuinfo ]; then
    cpu_line=$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null)
    if [ -n "$cpu_line" ]; then
        cpu_model=$(echo "$cpu_line" | cut -d: -f2- | xargs)
    else
        hw_line=$(grep -m1 'Hardware' /proc/cpuinfo 2>/dev/null)
        if [ -n "$hw_line" ]; then
            cpu_model=$(echo "$hw_line" | cut -d: -f2- | xargs)
        fi
    fi
fi
cpu_cores=$(nproc 2>/dev/null || echo "unknown")

# --- 3. Memory ---
memory_mb="unknown"
if [ -f /proc/meminfo ]; then
    mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null)
    if [ -n "$mem_kb" ] && [ "$mem_kb" -gt 0 ] 2>/dev/null; then
        memory_mb=$((mem_kb / 1024))
    fi
fi

# --- 4. Virtualization and container ---
virtualization="unknown"
container_engine="unknown"

if command -v systemd-detect-virt >/dev/null 2>&1; then
    virtualization=$(systemd-detect-virt 2>/dev/null)
fi

if [ -f /.dockerenv ]; then
    container_engine="docker"
elif [ -f /run/.containerenv ]; then
    container_engine="podman"
elif [ -d /lxc ] || [ -n "${container+x}" ] || [ "$virtualization" = "lxc" ]; then
    container_engine="lxc"
elif [ -f /proc/vz/version ] || [ -d /proc/vz ]; then
    container_engine="openvz"
fi

if [ "$virtualization" = "unknown" ]; then
    if grep -q '^flags.* hypervisor' /proc/cpuinfo 2>/dev/null; then
        virtualization="vm"
    else
        virtualization="physical"
    fi
fi

# --- 5. System identifiers ---
system_serial="unknown"
system_uuid="unknown"
if [ -r /sys/class/dmi/id/product_serial ] && [ "$(safe_read /sys/class/dmi/id/product_serial)" != "" ]; then
    system_serial=$(safe_read /sys/class/dmi/id/product_serial)
fi
if [ -r /sys/class/dmi/id/product_uuid ]; then
    system_uuid=$(safe_read /sys/class/dmi/id/product_uuid)
fi

# --- 6. Timezone ---
timezone="unknown"
if [ -L /etc/localtime ]; then
    tz_link=$(readlink /etc/localtime 2>/dev/null)
    if [[ "$tz_link" == *zoneinfo/* ]]; then
        timezone="${tz_link##*/zoneinfo/}"
    fi
elif [ -f /etc/timezone ]; then
    timezone=$(cat /etc/timezone 2>/dev/null)
elif [ -f /etc/TZ ]; then
    timezone=$(cat /etc/TZ 2>/dev/null)
fi

# --- 7. glibc and compilers ---
glibc_version="unknown"
gcc_version="unknown"
ldd_output=$(ldd --version 2>/dev/null | head -n1)
if [[ "$ldd_output" =~ [0-9]+\.[0-9]+ ]]; then
    glibc_version="${BASH_REMATCH[0]}"
fi
if command -v gcc >/dev/null 2>&1; then
    gcc_version=$(gcc -dumpversion 2>/dev/null)
fi

# --- 8. User and sudo ---
current_user=$(whoami 2>/dev/null || echo "unknown")
has_sudo="no"
if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    has_sudo="yes"
fi

# --- 9. Nix ---
nix_version="unknown"
if command -v nix >/dev/null 2>&1; then
    nix_out=$(nix --version 2>/dev/null | head -n1)
    if [[ "$nix_out" =~ [0-9]+\.[0-9]+ ]]; then
        nix_version="${BASH_REMATCH[0]}"
    fi
fi

# --- 10. Filesystems (fixed!) ---
filesystems="unknown"
if [ -f /proc/filesystems ]; then
    fs_list=$(awk '
        NF == 1 { print $1 }
        NF == 2 && $1 == "nodev" && $2 ~ /^(ext4|ext3|ext2|xfs|btrfs|vfat|ntfs|exfat)$/ { print $2 }
    ' /proc/filesystems 2>/dev/null | paste -sd, -)
    if [ -n "$fs_list" ]; then
        filesystems="$fs_list"
    fi
fi

# --- 11. DNS ---
dns_servers="unknown"
if [ -f /etc/resolv.conf ]; then
    ns_list=$(grep '^nameserver' /etc/resolv.conf 2>/dev/null | awk '{print $2}' | paste -sd, -)
    if [ -n "$ns_list" ]; then
        dns_servers="$ns_list"
    fi
fi

# --- 12. Security ---
apparmor="unknown"
selinux="unknown"
if [ -f /sys/kernel/security/apparmor/profiles ]; then
    apparmor="enabled"
else
    apparmor="disabled"
fi
if command -v getenforce >/dev/null 2>&1; then
    selinux=$(getenforce 2>/dev/null)
fi

# --- OUTPUT GENERAL FACTS ---
echo "os.name=$os_name"
echo "os.id=$os_id"
echo "kernel.version=$kernel_version"
echo "architecture=$architecture"
echo "hostname=$hostname"
echo "uptime.days=$uptime_days"
echo "cpu.model=$cpu_model"
echo "cpu.cores=$cpu_cores"
echo "memory.mb=$memory_mb"
echo "virtualization.type=$virtualization"
echo "container.engine=$container_engine"
echo "system.serial=$system_serial"
echo "system.uuid=$system_uuid"
echo "system.timezone=$timezone"
echo "system.glibc_version=$glibc_version"
echo "system.gcc_version=$gcc_version"
echo "user.current=$current_user"
echo "user.has_sudo=$has_sudo"
echo "nix.version=$nix_version"
echo "storage.filesystems=$filesystems"
echo "network.dns_servers=$dns_servers"
echo "security.apparmor=$apparmor"
echo "security.selinux=$selinux"

# --- 13. DISKS (one by one) ---
if command -v lsblk >/dev/null 2>&1; then
    lsblk -b -d -o NAME,SIZE,TYPE -n 2>/dev/null | while read -r name size type _; do
        if [ "$size" -eq 0 ] 2>/dev/null; then
            continue
        fi
        if [ "$type" != "disk" ]; then
            continue
        fi
        case "$name" in 
            nbd*|zram*|loop*|sr*|fd*|md*|ram*) 
                continue 
                ;;
        esac
        if [ -n "$name" ]; then
            if [ "$size" -ge 1099511627776 ]; then
                val="$((size / 1099511627776))TB"
            elif [ "$size" -ge 1073741824 ]; then
                val="$((size / 1073741824))GB"
            elif [ "$size" -ge 1048576 ]; then
                val="$((size / 1048576))MB"
            else
                val="${size}B"
            fi
            echo "disk.$name=$val"
        fi
    done
fi

# --- 14. NETWORK: interfaces with IPv4 ---
if command -v ip >/dev/null 2>&1; then
    ip -4 -br addr show 2>/dev/null | while read -r iface state ip_mask _; do
        if [ "$state" != "DOWN" ] && [ -n "$ip_mask" ] && [ "$ip_mask" != "-" ]; then
            ip_addr="${ip_mask%/*}"
            case "$ip_addr" in
                ""|"-"|*:*) 
                    continue 
                    ;;
            esac
            echo "interface.$iface=$ip_addr"
        fi
    done
fi
