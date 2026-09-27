{
  config,
  lib,
  pkgs,
  modulesPath,
  ...
}:
{
  imports = [
    ./vm-base.nix
    ./module
    (modulesPath + "/virtualisation/qemu-vm.nix")
  ];

  # enable Styx and the binary cache
  services.styx.enable = true;
  services.styx.enableStyxNixCache = true;

  # let styx handle everything
  nix.settings.styx-ondemand = [ ".*" ];

  # use shared nixpkgs
  nix.nixPath = [ "nixpkgs=/tmp/nixpkgs" ];

  # just console
  virtualisation.graphics = false;
  # nix invocations in the vm need a lot of ram
  virtualisation.memorySize = 4096;
  # provide nixpkgs and this dir for convenience
  virtualisation.sharedDirectories = {
    nixpkgs = {
      source = toString pkgs.path;
      target = "/tmp/nixpkgs";
    };
    styxsrc = {
      source = toString ./.;
      target = "/tmp/styxsrc";
    };
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

  # hack to transfer console size
  systemd.services."serial-getty@".serviceConfig.ExecStartPost =
    let
      fixconsole = pkgs.writeShellScript "fixconsole" ''
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
      '';
    in
    "-${fixconsole} %i";

  # auto-login as root
  services.getty.autologinUser = "root";

  # auto-init with test1 params
  systemd.services."StyxInitTest1" = {
    description = "Init Styx Nix storage manager";
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      ExecStart = "/run/current-system/sw/bin/StyxInitTest1";
      Type = "oneshot";
    };
  };
}
