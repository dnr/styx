{ config, lib, pkgs, ... }:
let
  styxtest = (import ./. { inherit pkgs; }).styx-test;
  runstyxtest = pkgs.writeShellScriptBin "runstyxtest" ''
    cd ${styxtest}/bin
    if [[ $UID != 0 ]]; then sudo=sudo; fi
    exec $sudo ./styxtest -test.v "$@"
  '';
in
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
    runstyxtest
  ];

  documentation.doc.enable = false;
  documentation.info.enable = false;
  documentation.man.enable = false;
  documentation.nixos.enable = false;

  system.stateVersion = "23.11";
}
