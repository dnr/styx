{ config, lib, pkgs, ... }:
let
  styxtest = (import ./. { inherit pkgs; }).styx-test;
  runstyxtest = pkgs.writeShellScriptBin "runstyxtest" ''
    cd ${styxtest}/bin
    if [[ $UID != 0 ]]; then sudo=sudo; fi
    exec $sudo ./styxtest -test.v "$@"
  '';
in {
  imports = [
    ./vm-base.nix
    ./module
  ];

  # test suite needs only kernel options (and internet access)
  services.styx.enableKernelOptions = true;

  environment.systemPackages = with pkgs; [
    psmisc # for fuser
    runstyxtest
  ];
}
