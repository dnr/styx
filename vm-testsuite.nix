{
  config,
  lib,
  pkgs,
  fstype,
  ...
}:
let
  styxtest = (import ./. { inherit pkgs; }).styx-test;
  runstyxtest = pkgs.writeShellScriptBin "runstyxtest" ''
    cd ${styxtest}/bin
    if [[ $UID != 0 ]]; then sudo=sudo; fi
    $sudo modprobe nbd
    exec $sudo ./styxtest -test.v "$@"
  '';
in
{
  imports = [
    ./vm-base.nix
    ./module
  ];

  # get just these to disable udev probing
  services.styx.udevRules = true;

  # set fstype of root fs
  virtualisation.fileSystems."/".fsType = lib.mkForce fstype;

  environment.systemPackages = with pkgs; [
    runstyxtest
  ];
}
