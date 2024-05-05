{ config, lib, pkgs, ... }:
{
  imports = [
    ./vm-base.nix
    ./module
    ./module/testsuite.nix
  ];

  # test suite needs only kernel options (and internet access)
  services.styx.enableKernelOptions = true;
}
