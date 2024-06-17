### https://channels.nixos.org/nixos-24.05 nixos

{ config, pkgs, ... }:
let
  src = pkgs.fetchFromGitHub {
    owner = "dnr";
    repo = "styx";
    rev = "5be11bc61319e3f1a0f15f1a556cbf8ac0c3e3ac";
    hash = "sha256-T2QXQCpP8NZwzg6Q74zVMTU8M7NA9oVSg0cRrkPPUSc=";
  };
  srci = import "${src}/ci" { inherit pkgs; };
in
{
  imports = [ <nixpkgs/nixos/modules/virtualisation/amazon-image.nix> ];
  systemd.services.charon = {
    description = "charon ci for styx";
    wantedBy = [ "multi-user.target" ];
    serviceConfig.ExecStart =
      "${srci.charon}/bin/charon worker --heavy --temporal_ssm styx-charon-temporal-params";
    serviceConfig.Restart = "always";
  };
}
