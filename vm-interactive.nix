{ config, lib, pkgs, ... }:
{
  imports = [
    ./vm-base.nix
    ./module
    <nixpkgs/nixos/modules/virtualisation/qemu-vm.nix>
    # ./qemu-vm-btrfs.nix
  ];

  # enable all options
  services.styx.enable = true;

  # let styx handle everything
  nix.settings.styx-include = [ ".*" ];

  # use shared nixpkgs
  nix.nixPath = [ "nixpkgs=/tmp/nixpkgs" ];

  # provide nixpkgs and this dir for convenience
  virtualisation.sharedDirectories = {
    nixpkgs = { source = toString <nixpkgs>; target = "/tmp/nixpkgs"; };
    styxsrc = { source = toString ./.;       target = "/tmp/styxsrc"; };
  };

  # more convenience
  environment.shellAliases = {
    l = "less";
    ll = "ls -l";
    g = "grep";
  };
  environment.variables = {
    TMPDIR = "/tmp"; # tmpfs is too small to build stuff
  };
  environment.systemPackages = with pkgs; [
    file
    jq
    psmisc
    vim
  ];
}
