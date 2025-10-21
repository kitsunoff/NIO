{ lib, ... }:

{
  disko.devices = {
    disk = {
      # Основной диск системы
      main = {
        type = "disk";
        device = "/dev/sda";
        content = {
          type = "gpt";
          partitions = {
            # EFI раздел
            boot = {
              size = "512M";
              type = "EF00";
              content = {
                type = "filesystem";
                format = "vfat";
                mountpoint = "/boot";
                mountOptions = [
                  "defaults"
                ];
              };
            };
            
            # Swap раздел
            swap = {
              size = "4G";
              content = {
                type = "swap";
                resumeDevice = true;
              };
            };
            
            # Корневой раздел
            root = {
              size = "100%";
              content = {
                type = "btrfs";
                extraArgs = [ "-f" ];
                subvolumes = {
                  # Корневой subvolume
                  "@" = {
                    mountpoint = "/";
                  };
                  
                  # Subvolume для nix store
                  "@nix" = {
                    mountpoint = "/nix";
                  };
                  
                  # Subvolume для home
                  "@home" = {
                    mountpoint = "/home";
                  };
                  
                  # Subvolume для var
                  "@var" = {
                    mountpoint = "/var";
                  };
                  
                  # Subvolume для log
                  "@log" = {
                    mountpoint = "/var/log";
                  };
                  
                  # Subvolume для tmp
                  "@tmp" = {
                    mountpoint = "/tmp";
                  };
                };
                
                mountOptions = [
                  "defaults"
                  "noatime"
                  "compress=zstd"
                  "ssd"
                  "space_cache=v2"
                  "subvol=@"
                ];
              };
            };
          };
        };
      };
    };
    
    # Альтернативная конфигурация для NVMe дисков
    disk.nvme = {
      type = "disk";
      device = "/dev/nvme0n1";
      content = {
        type = "gpt";
        partitions = {
          boot = {
            size = "512M";
            type = "EF00";
            content = {
              type = "filesystem";
              format = "vfat";
              mountpoint = "/boot";
            };
          };
          
          swap = {
            size = "8G";
            content = {
              type = "swap";
              resumeDevice = true;
            };
          };
          
          root = {
            size = "100%";
            content = {
              type = "btrfs";
              extraArgs = [ "-f" ];
              subvolumes = {
                "@" = {
                  mountpoint = "/";
                };
                "@nix" = {
                  mountpoint = "/nix";
                };
                "@home" = {
                  mountpoint = "/home";
                };
                "@var" = {
                  mountpoint = "/var";
                };
                "@log" = {
                  mountpoint = "/var/log";
                };
                "@tmp" = {
                  mountpoint = "/tmp";
                };
              };
              mountOptions = [
                "defaults"
                "noatime"
                "compress=zstd"
                "ssd"
                "space_cache=v2"
                "subvol=@"
              ];
            };
          };
        };
      };
    };
    
    # Конфигурация для ZFS (альтернативный вариант)
    disk.zfs = {
      type = "disk";
      device = "/dev/sdb";
      content = {
        type = "zfs";
        pool = "rpool";
      };
    };
    
    nodev = {
      "/" = {
        fsType = "tmpfs";
        mountOptions = [
          "defaults"
          "size=2G"
          "mode=755"
        ];
      };
    };
  };
}
