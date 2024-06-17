### https://channels.nixos.org/nixos-24.05 nixos

{ config, pkgs, lib, ... }:
let
  src = lib.fetchFromGitHub {
    owner = "dnr";
    repo = "styx";
    rev = "main";
  };
  srci = import "${src}/ci";
  pkg = srci.charon;
in
{
  imports = [ <nixpkgs/nixos/modules/virtualisation/amazon-image.nix> ];
  ec2.hvm = true;
  environment.systemPackages = with pkgs; [
    git
  ];

  systemd.services.charon = {
    description = "charon ci for styx";
    wantedBy = [ "multi-user.target" ];
    serviceConfig.ExecStart = "${charon}/bin/charon worker --heavyworker";
    serviceConfig.Restart = "always";
  };
}
