{ config, pkgs, ... }:

let
  # Публичный ключ для SSH доступа
    sshPrivateKeyFile =  ./flake-ssh-private-key;

   # Генерируем публичный ключ с помощью ssh-keygen из nixpkgs
    sshPublicKey = builtins.readFile (
          pkgs.runCommand "ssh-public-key" {} ''
            ${pkgs.openssh}/bin/ssh-keygen -y -f ${sshPrivateKeyFile} > $out
          ''
        );
in
{
  imports = [
    ./disko-config.nix
  ];

  # Ультра простая конфигурация
  networking.hostName = "ultra-server";
  time.timeZone = "Europe/Moscow";

  # Пользователи с SSH доступом
  users.users.root.openssh.authorizedKeys.keys = [ sshPublicKey ];
  users.users.kitsunoff = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    openssh.authorizedKeys.keys = [ sshPublicKey ];
    hashedPassword = "$y$j9T$1ufo60PHEVj7Y9t2WEnUH1$Ntha5GMZ7Ri6di6WzhosBp0.t253AlTQfpbF8zsfaq3";
  };

  # SSH сервер
  services.openssh = {
    enable = true;
    settings.PasswordAuthentication = true;
  };

  # Файрвол - только SSH
  networking.firewall = {
    enable = true;
    allowedTCPPorts = [ 22 ];
  };

  # Минимальный набор пакетов
  environment.systemPackages = with pkgs; [
    vim
    curl
    wget
    htop
  ];

  # Nix с флейками
  nix = {
    package = pkgs.nixVersions.stable;
    extraOptions = "experimental-features = nix-command flakes";
    settings.auto-optimise-store = true;
  };

  # Bootloader
  boot.loader = {
    systemd-boot.enable = true;
    efi.canTouchEfiVariables = true;
  };

  system.stateVersion = "23.11";
}
