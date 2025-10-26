{ config, pkgs, ... }:

let
     # Публичный ключ для SSH доступа
    sshPrivateKeyFile = ./flake-ssh-private-key;
   # Генерируем публичный ключ с помощью ssh-keygen из nixpkgs
    sshPublicKey = builtins.readFile (
          pkgs.runCommand "ssh-public-key" {} ''
            ${pkgs.openssh}/bin/ssh-keygen -y -f ${sshPrivateKeyFile} > $out
          ''
        );
in
{
  # Базовая минимальная конфигурация для отката
  networking.hostName = "minimal-server";
  time.timeZone = "Europe/Moscow";
  imports = [
    ./disko-config.nix
  ];

  # Пользователи и SSH доступ
  users.users.root.openssh.authorizedKeys.keys = [
    sshPublicKey
  ];
  users.users.kitsunoff = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    openssh.authorizedKeys.keys = [ sshPublicKey ];
  };

  # Включение SSH сервера
  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      PermitRootLogin = "prohibit-password";
    };
  };

  # Сетевые настройки
  networking.firewall = {
    enable = true;
    allowedTCPPorts = [ 22 ];
    allowedUDPPorts = [ ];
  };

  # Минимальный набор пакетов
  environment.systemPackages = with pkgs; [
    vim
    curl
    wget
  ];

  # Nix настройки
  nix = {
    package = pkgs.nixVersions.stable;
    extraOptions = ''
      experimental-features = nix-command flakes
    '';
    settings = {
      auto-optimise-store = true;
      trusted-users = [ "root" ];
    };
  };

  # Bootloader
  boot.loader = {
    systemd-boot.enable = true;
    efi.canTouchEfiVariables = true;
  };

  # Системные настройки
  system.stateVersion = "23.11";
}
