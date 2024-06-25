# This file is copied to /etc/nixos/configuration.nix and the config is build and activated.
# Note this is templated by terraform, $ {} is TF, $${} is Nix.
{ config, pkgs, ... }:
{
  imports = [ <nixpkgs/nixos/modules/virtualisation/amazon-image.nix> ];

  nix.settings.substituters = [ "${sub}" ];
  nix.settings.trusted-public-keys = [ "${pubkey}" ];

  systemd.services.charon = {
    description = "charon ci worker";
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      # TODO: this works but it feels weird. should it use builtins.storePath?
      # but how do we set the cache and trusted key in that case?
      ExecStartPre = "$${pkgs.nix}/bin/nix-store --realize ${charon}";
      ExecStart = "${charon}/bin/charon worker --heavy --temporal_ssm ${tmpssm} --cache_signkey_ssm ${cachessm} --chunkbucket ${bucket} --styx_signkey_ssm ${styxssm}";
      Restart = "always";
    };
  };
}
