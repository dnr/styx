{ config, lib, pkgs, ... }:
{
  imports = [
    ./vm-base.nix
    ./module
    <nixpkgs/nixos/modules/virtualisation/qemu-vm.nix>
  ];

  # enable all options
  services.styx.enable = true;

  # let styx handle everything
  nix.settings.styx-include = [ ".*" ];

  # provide nixpkgs and this dir for convenience
  virtualisation.sharedDirectories = {
    nixpkgs = { source = toString <nixpkgs>; target = "/tmp/nixpkgs"; };
    styxsrc = { source = toString ./.;       target = "/tmp/styxsrc"; };
  };
}
