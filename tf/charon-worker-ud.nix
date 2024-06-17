### https://channels.nixos.org/nixos-24.05 nixos

{ config, pkgs, ... }:
let
  src = pkgs.fetchFromGitHub {
    owner = "dnr";
    repo = "styx";
    rev = "cdfb7e7f29d973f1cbb30f138ad7d7ee418b4507";
    # nix-prefetch-url --unpack https://github.com/dnr/styx/archive/$rev.tar.gz
    sha256 = "1mhcdvzijk94yl8q34k5s81kc6lxcj9cncb50g5cpkvxks73x54p";
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
