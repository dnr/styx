{ config, lib, pkgs, ... }:
let
  defpkg = (import ../. { inherit pkgs; }).styx-local;
  cfg = config.services.styx;
in with lib; {
  options = {
    services.styx = {
      enable = mkEnableOption "Styx storage manager for nix";
      enablePatchedNix = mkEnableOption "Patched nix for styx";
      enableNixSettings = mkEnableOption "nix.conf settings for styx";
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
        default = defpkg;
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

        # Use binary cache to avoid rebuilds:
        extra-substituters = [ "https://styx-1.s3.amazonaws.com/nixcache/" ];
        extra-trusted-public-keys = [ "styx-nixcache-test-1:IbJB9NG5antB2WpE+aE5QzmXapT2yLQb8As/FRkbm3Q=" ];

        styx-include = [ ];

        # These are defaults:
        #styx-min-size = 32*1024;
        #styx-sock-path = "/var/cache/styx/styx.sock";
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
          #TemporaryFileSystem = "/tmpfs:size=16G,mode=1777"; # force tmpfs
        };
        #environment.TMPDIR = "/tmpfs";
      };
    })
  ];
}
