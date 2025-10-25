{ lib, ... }:

{
  disko.devices.disk.nvme = {
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

        # Всё остальное — один btrfs-раздел
        root = {
          size = "100%";  # ← занимает всё оставшееся место
          content = {
            type = "btrfs";
            extraArgs = [ "-f" ];
            subvolumes = {
              # Системные подтома
              "@" = { mountpoint = "/"; };
              "@nix" = { mountpoint = "/nix"; };
              "@home" = { mountpoint = "/home"; };
              "@var" = { mountpoint = "/var"; };
              "@log" = { mountpoint = "/var/log"; };
              "@tmp" = { mountpoint = "/tmp"; };

              # Под контейнеры, K8s, образы и т.д.
              "@containers" = {
                mountpoint = "/var/lib/containers";
                # или "/var/lib/docker", "/var/lib/kubelet" — как нужно
              };
            };

            # Опции монтирования для корня (subvol=@)
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
}