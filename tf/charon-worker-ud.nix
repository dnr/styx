# This file is copied to /etc/nixos/configuration.nix and the config is build and activated.
# Note this is templated by terraform, $ {} is TF, $${} is Nix.
{ config, pkgs, ... }:
{
  imports = [ <nixpkgs/nixos/modules/virtualisation/amazon-image.nix> ];
  systemd.services.charon = {
    description = "charon ci worker";
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      # TODO: this works but it feels weird. should it use builtins.storePath?
      # but how do we set the cache and trusted key in that case?
      ExecStartPre = "nix-store --realize --option extra-substituters ${sub} --option extra-trusted-public-keys ${pubkey} ${charon}";
      ExecStart = "${charon}/bin/charon worker --heavy --temporal_ssm ${tmpssm}";
      Restart = "always";
    };
  };
}
