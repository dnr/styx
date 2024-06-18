{ pkgs ? import <nixpkgs> { } }:
rec {
  # by deploy-ci, for heavy worker on EC2
  charon = pkgs.buildGoModule {
    pname = "charon";
    version = "0.0.1";
    vendorHash = "sha256-XJMP7w5bsN5Lo0c0ejM8wNKC2tvSSzqNYqULDej0hoo=";
    src = pkgs.lib.sourceByRegex ./.. [
      "^go\.(mod|sum)$"
      "^(cmd|cmd/charon|cmd/charon/.*)$"
      "^(ci|common|pb)($|/.*)"
    ];
    subPackages = [ "cmd/charon" ];
    doCheck = false;
  };

  # for light worker on non-AWS server:
  charon-image = pkgs.dockerTools.streamLayeredImage {
    name = "charon-worker-light";
    contents = [
      pkgs.cacert
    ];
    config = {
      User = "1000:1000";
      Entrypoint = [ "${charon}/bin/charon" ];
      Cmd = [ "worker" "--worker" ];
    };
  };
}
