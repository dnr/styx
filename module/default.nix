{ config, lib, pkgs, ... }:
let
  defpkg = (import ../. { inherit pkgs; }).styx-local;
  cfg = config.services.styx;
in with lib; {
  options = {
    services.styx = {
      enable = mkEnableOption "Nix storage manager";
      params = mkOption {
        description = "url to remote params";
        type = types.str;
        default = "https://styx-1.s3.amazonaws.com/params-dev-1";
      };
      keys = mkOption {
        description = "signing keys";
        type = types.listOf types.str;
        default = ["styx-dev-1:SCMYzQjLTMMuy/MlovgOX0rRVCYKOj+cYAfQrqzcLu0="];
      };
      package = mkOption {
        description = "styx package";
        type = types.package;
        default = defpkg;
      };
    };
  };

  config = mkIf cfg.enable {
    nix.settings = {
      # Add "?styx=1" to default substituter.
      # TODO: can we do this in overlay style to filter the previous value
      substituters = ["https://cache.nixos.org/?styx=1"];

      # Use binary cache to avoid rebuilds:
      extra-substituters = "https://styx-1.s3.amazonaws.com/nixcache/";
      extra-trusted-public-keys = "styx-nixcache-dev-1:IbJB9NG5antB2WpE+aE5QzmXapT2yLQb8As/FRkbm3Q=";

      # These are defaults:
      #styx-min-size = 32*1024;
      #styx-sock-path = "/var/cache/styx/styx.sock";
    };

    # Need to turn on these kernel config options. This requires building the whole kernel :(
    boot.kernelPatches = [ {
      name = "styx";
      patch = null;
      extraStructuredConfig = {
        CACHEFILES_ONDEMAND = lib.kernel.yes;
        EROFS_FS_ONDEMAND = lib.kernel.yes;
      };
    } ];

    systemd.services.styx = {
      description = "Nix storage manager";
      serviceConfig.ExecStart = let
        keys = lib.lists.concatMapStringsSep " " (k: "--styx_pubkey ${k}");
        flags = "--params ${cfg.params} ${keys};"
      in
        "${cfg.package}/bin/styx ${flags}";
      serviceConfig.Type = "notify";
      serviceConfig.NotifyAccess = "all";
      #serviceConfig.TemporaryFileSystem = "/tmpfs:size=16G,mode=1777"; # force tmpfs
      #environment.TMPDIR = "/tmpfs";
    };
  };
}
