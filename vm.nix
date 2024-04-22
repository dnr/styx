{ config, lib, pkgs, ... }:
{
  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = true;

  boot.kernelPackages = pkgs.linuxPackages_latest;
  boot.kernelPatches = [ {
    name = "styx";
    patch = null;
    extraStructuredConfig = {
      CACHEFILES_ONDEMAND = lib.kernel.yes;
      EROFS_FS_ONDEMAND = lib.kernel.yes;
    };
  } ];

  networking.hostName = "testvm";
  networking.networkmanager.enable = true;

  users.users.test = {
    isNormalUser = true;
    initialPassword = "test";
    extraGroups = [ "wheel" ];
  };

  security.sudo.wheelNeedsPassword = false;

  environment.systemPackages = with pkgs; [
    psmisc # for fuser

    (let test = (import ./. { inherit pkgs; }).styx-test; in (pkgs.writeShellScriptBin "runstyxtest" ''
      cd ${test}/bin
      if [[ $UID != 0 ]]; then sudo=sudo; fi
      exec $sudo ./styxtest
    ''))
  ];

  documentation.doc.enable = false;
  documentation.info.enable = false;
  documentation.man.enable = false;
  documentation.nixos.enable = false;

  system.stateVersion = "23.11";
}
