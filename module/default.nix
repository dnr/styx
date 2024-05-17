{ config, lib, pkgs, ... }:
let
  styx = import ../. { inherit pkgs; };
  cfg = config.services.styx;
in with lib; {
  options = {
    services.styx = {
      enable = mkEnableOption "Styx storage manager for nix";
      enablePatchedNix = mkEnableOption "Patched nix for styx";
      enableNixSettings = mkEnableOption "nix.conf settings for styx";
      enableStyxNixCache = mkEnableOption "add binary cache for styx and related packages";
      enableKernelOptions = mkEnableOption "Enable erofs+cachefiles on-demand kernel options for styx";
      params = mkOption {
        description = "url to remote params";
        type = types.str;
        default = "https://styx-1.s3.amazonaws.com/params/test-1";
      };
      keys = mkOption {
        description = "signing keys";
        type = types.listOf types.str;
        default = ["styx-test-1:bmMrKgN5yF3dGgOI67TZSfLts5IQHwdrOCZ7XHcaN+w="];
      };
      package = mkOption {
        description = "styx package";
        type = types.package;
        default = styx.styx-local;
      };
    };
  };

  config = mkMerge [
    (mkIf (cfg.enable || cfg.enablePatchedNix) {
      nix.package = styx.patched-nix;
    })

    (mkIf (cfg.enable || cfg.enableNixSettings) {
      nix.settings = {
        # Add "?styx=1" to default substituter.
        # TODO: can we do this in overlay style to filter the previous value?
        substituters = mkForce [ "https://cache.nixos.org/?styx=1" ];
        styx-include = [ ];
      };
    })

    (mkIf (cfg.enable || cfg.enableStyxNixCache) {
      nix.settings = {
        # Use binary cache to avoid rebuilds:
        extra-substituters = [ "https://styx-1.s3.amazonaws.com/nixcache/" ];
        extra-trusted-public-keys = [ "styx-nixcache-test-1:IbJB9NG5antB2WpE+aE5QzmXapT2yLQb8As/FRkbm3Q=" ];
      };
    })

    (mkIf (cfg.enable || cfg.enableKernelOptions) {
      # Need to turn on these kernel config options. This requires building the whole kernel :(
      boot.kernelPackages = pkgs.linuxPackages_latest;
      boot.kernelPatches = [ {
        name = "styx";
        patch = null;
        extraStructuredConfig = {
          CACHEFILES_ONDEMAND = lib.kernel.yes;
          EROFS_FS_ONDEMAND = lib.kernel.yes;
        };
      } ];
    })

    (mkIf cfg.enable {
      environment.systemPackages = [ cfg.package ];

      systemd.services.styx = {
        description = "Nix storage manager";
        wantedBy = [ "multi-user.target" ];
        after = [ "network-online.target" ];
        serviceConfig = {
          ExecStart = let
            keys = strings.concatMapStringsSep " " (k: "--styx_pubkey ${k}") cfg.keys;
            flags = "--params ${cfg.params} ${keys}";
          in "${cfg.package}/bin/styx daemon ${flags}";
          Type = "notify";
          NotifyAccess = "all";
          FileDescriptorStoreMax = "1";
          FileDescriptorStorePreserve = "yes";
          LimitNOFILE = "500000";
          Restart = "on-failure";
        };
      };
    })
  ];
}
