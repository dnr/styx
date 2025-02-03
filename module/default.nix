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
      nix.package = styx.patchedNix;
    })

    (mkIf (cfg.enable || cfg.enableNixSettings) {
      nix.settings = {
        # Add "?styx=1" to default substituter.
        # TODO: can we do this in overlay style to filter the previous value?
        substituters = mkForce [ "https://cache.nixos.org/?styx=1" ];
        styx-ondemand = [ ];
        styx-materialize = [ ];
      };
    })

    (mkIf (cfg.enable || cfg.enableStyxNixCache) {
      nix.settings = {
        # Use binary cache to avoid rebuilds:
        extra-substituters = [ "https://styx-1.s3.amazonaws.com/nixcache/?styx=1" ];
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
        requires = [
          "modprobe@cachefiles.service"
        ];
        after = [
          "local-fs.target"
          "modprobe@cachefiles.service"
        ];
        # TODO: restartTriggers?
        unitConfig = {
          DefaultDependencies = false;
          RequiresMountsFor = [ "/var/cache/styx" "/nix/store" ];
        };
        serviceConfig = {
          # Use unshare directly instead of PrivateMounts so that our new mounts
          # are propagated normally, but we can remount /nix/store rw.
          ExecStart = "${pkgs.util-linux}/bin/unshare -m --propagation unchanged ${cfg.package}/bin/styx daemon";
          SyslogIdentifier = "styx";
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
