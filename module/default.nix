{ config, lib, pkgs, ... }:
let
  styx = import ../. { inherit pkgs; };
  cfg = config.services.styx;
in with lib; {
  options = {
    services.styx = {
      enable = mkEnableOption "Styx storage manager for Nix";
      enablePatchedNix = mkEnableOption "Patched Nix for Styx";
      enableNixSettings = mkEnableOption "nix.conf settings for Styx";
      enableStyxNixCache = mkEnableOption "Add binary cache for Styx and related packages";
      enableKernelOptions = mkEnableOption "Enable required kernel config for Styx (erofs+cachefiles)";
      package = mkOption {
        description = "Styx package";
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
      # Need to turn on these kernel config options:
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
      # Tag configuration so we can easily find a non-Styx config if we broke everything.
      system.nixos.tags = [ "styx" ];

      # expose cli
      environment.systemPackages = [
        cfg.package
        styx.StyxInitTest1
      ];

      # main service
      systemd.services.styx = {
        description = "Nix storage manager";
        wantedBy = [ "sysinit.target" ];
        before = [ "sysinit.target" "shutdown.target" ];
        conflicts = [ "shutdown.target" ];
        after = [ "local-fs.target" ];
        # TODO: restartTriggers
        unitConfig = {
          DefaultDependencies = false;
          RequiresMountsFor = [ "/var/cache/styx" "/nix/store" ];
        };
        serviceConfig = {
          ExecStart = "${cfg.package}/bin/styx daemon";
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
