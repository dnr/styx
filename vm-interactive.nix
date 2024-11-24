{ config, lib, pkgs, ... }:
{
  imports = [
    ./vm-base.nix
    ./module
    <nixpkgs/nixos/modules/virtualisation/qemu-vm.nix>
  ];
  assertions = [ {
    assertion = config.virtualisation.diskImage != null;
    message = "must use disk image";
  } ];

  # enable all options
  services.styx.enable = true;

  # let styx handle everything
  nix.settings.styx-include = [ ".*" ];

  # use shared nixpkgs
  nix.nixPath = [ "nixpkgs=/tmp/nixpkgs" ];

  # just console
  virtualisation.graphics = false;
  # provide nixpkgs and this dir for convenience
  virtualisation.sharedDirectories = {
    nixpkgs = { source = toString <nixpkgs>; target = "/tmp/nixpkgs"; };
    styxsrc = { source = toString ./.;       target = "/tmp/styxsrc"; };
  };
  # set fstype of root fs
  virtualisation.fileSystems."/".fsType = lib.mkForce (builtins.getEnv "VMFSTYPE");
  # ensure btrfs enabled
  system.requiredKernelConfig = with config.lib.kernelConfig; [ (isEnabled "BTRFS_FS") ];

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

  # hack to transfer console size
  systemd.services."serial-getty@".serviceConfig.ExecStartPost =
    let fixconsole = pkgs.writeShellScript "fixconsole" ''
      #!${pkgs.runtimeShell}
      tty=/dev/$1
      for o in $(</proc/cmdline); do
        case $o in
          styx.consolesize=*)
            set -- $(IFS=:=; echo $o)
            ${pkgs.coreutils}/bin/stty -F $tty rows $2 cols $3
            echo -ne '\e[?7h' > $tty
            ;;
        esac
      done
    ''; in "-${fixconsole} %i";
}
