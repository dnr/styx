# Note this is templated by terraform, $ {} is TF, $${} is Nix
{ config, pkgs, ... }:
{
  imports = [ <nixpkgs/nixos/modules/virtualisation/amazon-image.nix> ];
  systemd.services.charon = {
    description = "charon ci worker";
    wantedBy = [ "multi-user.target" ];
    environment.AWS_REGION = "${region}";
    serviceConfig = {
      ExecStartPre = "nix-store --realize --option extra-substituters ${sub} --option extra-trusted-public-keys ${pubkey} ${charon}";
      ExecStart = "${charon}/bin/charon worker --heavy --temporal_ssm ${tmpssm}";
      Restart = "always";
    };
  };
}
