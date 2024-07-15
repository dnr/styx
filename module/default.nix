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
      nix.package = pkgs.nixVersions.nix_2_18.overrideAttrs (prev: {
        patches = prev.patches ++ [ ./patches/nix_2_18.patch ];
        doInstallCheck = false; # broke tests/ca, ignore for now
      });
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
      assertions = [ {
          assertion = lib.versionAtLeast config.boot.kernelPackages.kernel.version "6.8";
          message = "Styx requires at least a 6.8 kernel";
      } ];
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
        wants = [ "network-online.target" ];
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
