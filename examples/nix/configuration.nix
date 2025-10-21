{ config, pkgs, ... }:

let
  # Публичный ключ для SSH доступа
   sshPublicKey = builtins.readFile ./flake-ssh-key.pub;
in
{
  imports = [
    ./disko-config.nix
  ];

  # Ультра простая конфигурация
  networking.hostName = "ultra-server";
  time.timeZone = "Europe/Moscow";

  # Только root пользователь с SSH доступом
  users.users.root.openssh.authorizedKeys.keys = [ sshPublicKey ];

  # SSH сервер
  services.openssh = {
    enable = true;
    settings.PasswordAuthentication = false;
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
    package = pkgs.nixFlakes;
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
