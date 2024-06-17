{ pkgs ? import <nixpkgs> { config = {}; overlays = []; } }:
rec {
  base = {
    pname = "styx";
    version = "0.0.6";
    vendorHash = "sha256-TB0zFm95H8R/tHfnjYK90TjpX5tqKnLohDBS9qXGzds=";
    src = pkgs.lib.sourceByRegex ./. [
      "^go\.(mod|sum)$"
      "^(cmd|cmd/styx|cmd/styx/.*)$"
      "^(common|daemon|erofs|manifester|pb|keys|tests)($|/.*)"
    ];
    subPackages = [ "cmd/styx" ];
    ldflags = with pkgs; [
      "-X github.com/dnr/styx/common.NixBin=${nix}/bin/nix"
      "-X github.com/dnr/styx/common.GzipBin=${gzip}/bin/gzip"
      "-X github.com/dnr/styx/common.XzBin=${xz}/bin/xz"
      "-X github.com/dnr/styx/common.ModprobeBin=${kmod}/bin/modprobe"
      "-X github.com/dnr/styx/common.Version=${base.version}"
    ];
  };

  styx-local = pkgs.buildGoModule (base // {
  });

  styx-test = pkgs.buildGoModule (base // {
    pname = "styxtest";
    src = pkgs.lib.sourceByRegex ./. [
      "^go\.(mod|sum)$"
      "^(cmd|common|daemon|erofs|manifester|pb|keys|tests)($|/.*)"
    ];
    doCheck = false;
    buildPhase = ''
      go test -c -o styxtest ./tests
    '';
    installPhase = ''
      mkdir -p $out/bin $out/keys
      install styxtest $out/bin/
      cp keys/testsuite* $out/keys/
    '';
  });

  styx-lambda = pkgs.buildGoModule (base // {
    tags = [ "lambda.norpc" ];
    ldflags = [
      # "-s" "-w"  # only saves 3.6% of image size
      "-X github.com/dnr/styx/common.XzBin=${xzStaticBin}/bin/xz"
      # GzipBin is not used by manifester or differ, only local
      "-X github.com/dnr/styx/common.Version=${base.version}"
    ];
  });

  patched-nix = pkgs.nixVersions.nix_2_18.overrideAttrs (prev: {
    patches = prev.patches ++ [ ./module/patches/nix_2_18.patch ];
    doInstallCheck = false; # broke tests/ca, ignore for now
  });

  # Use static binaries and take only the main binaries to make the image as
  # small as possible:
  xzStaticBin = pkgs.stdenv.mkDerivation {
    name = "xz-binonly";
    src = pkgs.pkgsStatic.xz;
    installPhase = "mkdir -p $out/bin && cp $src/bin/xz $out/bin/";
  };

  image = pkgs.dockerTools.streamLayeredImage {
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
}
