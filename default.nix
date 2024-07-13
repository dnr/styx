{ pkgs ? import <nixpkgs> { config = {}; overlays = []; } }:
rec {
  base = {
    pname = "styx";
    version = "0.0.6";
    vendorHash = "sha256-5bGVxgSbk+R4ioc8Kx2VVwPv0Q/GaxbY5vPiLNDauPA=";
    src = pkgs.lib.sourceByRegex ./. [
      "^go\.(mod|sum)$"
      "^(ci|cmd|common|daemon|erofs|manifester|pb|keys|tests)($|/.*)"
    ];
    subPackages = [ "cmd/styx" ];
    doCheck = false;
    ldflags = baseLdFlags;
  };

  baseLdFlags = [
    "-X github.com/dnr/styx/common.NixBin=${pkgs.nix}/bin/nix"
    "-X github.com/dnr/styx/common.XzBin=${pkgs.xz}/bin/xz"
    "-X github.com/dnr/styx/common.Version=${base.version}"
  ];
  daemonLdFlags = baseLdFlags ++ [
    "-X github.com/dnr/styx/common.GzipBin=${pkgs.gzip}/bin/gzip"
    "-X github.com/dnr/styx/common.ModprobeBin=${pkgs.kmod}/bin/modprobe"
  ];
  staticLdFlags = [
    # "-s" "-w"  # only saves 3.6% of image size
    "-X github.com/dnr/styx/common.XzBin=${xzStaticBin}/bin/xz"
    # GzipBin is not used by manifester or differ, only local
    "-X github.com/dnr/styx/common.Version=${base.version}"
  ];

  styx-local = pkgs.buildGoModule (base // {
    ldflags = daemonLdFlags;
  });

  styx-lambda = pkgs.buildGoModule (base // {
    tags = [ "lambda.norpc" ];
    ldflags = staticLdFlags;
  });

  styx-test = pkgs.buildGoModule (base // {
    pname = "styxtest";
    buildPhase = ''
      go test -c -o styxtest ./tests
    '';
    installPhase = ''
      mkdir -p $out/bin $out/keys
      install styxtest $out/bin/
      cp keys/testsuite* $out/keys/
    '';
    ldflags = daemonLdFlags;
  });

  # built by deploy-ci, for heavy CI worker on EC2
  charon = pkgs.buildGoModule (base // {
    pname = "charon";
    subPackages = [ "cmd/charon" ];
  });

  # Use static binaries and take only the main binaries to make the image as
  # small as possible:
  xzStaticBin = pkgs.stdenv.mkDerivation {
    name = "xz-binonly";
    src = pkgs.pkgsStatic.xz;
    installPhase = "mkdir -p $out/bin && cp $src/bin/xz $out/bin/";
  };

  # for styx lambda manifester and chunk differ:
  styx-lambda-image = pkgs.dockerTools.streamLayeredImage {
    name = "lambda";
    # TODO: can we make it run on arm?
    # architecture = "arm64";
    contents = [
      pkgs.cacert
    ];
    config = {
      User = "1000:1000";
      Entrypoint = [ "${styx-lambda}/bin/styx" ];
    };
  };

  # for light CI worker on non-AWS server (dd5):
  charon-image = pkgs.dockerTools.streamLayeredImage {
    name = "charon-worker-light";
    contents = [
      pkgs.cacert
    ];
    config = {
      User = "1000:1000";
      Entrypoint = [ "${charon}/bin/charon" ];
    };
  };
}
