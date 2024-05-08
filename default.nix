{ pkgs ? import <nixpkgs> { config = {}; overlays = []; } }:
rec {
  base = {
    pname = "styx";
    version = "0.0.6";
    vendorHash = "sha256-VFjz+0tsI+4/nLG93SjJzyDlu4mnZJJAoF2mihrGVps=";
    src = pkgs.lib.sourceByRegex ./. [
      "^go\.(mod|sum)$"
      "^(cmd|common|daemon|erofs|manifester|pb)($|/.*)"
    ];
    subPackages = [ "cmd/styx" ];
    ldflags = with pkgs; [
      "-X github.com/dnr/styx/common.GzipBin=${gzip}/bin/gzip"
      "-X github.com/dnr/styx/common.NixBin=${nix}/bin/nix"
      "-X github.com/dnr/styx/common.XzBin=${xz}/bin/xz"
      "-X github.com/dnr/styx/common.ZstdBin=${zstd}/bin/zstd"
      "-X github.com/dnr/styx/common.ModprobeBin=${kmod}/bin/modprobe"
      "-X github.com/dnr/styx/common.Version=${base.version}"
    ];
  };

  styx-local = pkgs.buildGoModule (base // {
    # buildInputs = with pkgs; [
    #   brotli.dev
    # ];
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
    # CGO is only needed for cbrotli, which is only used on the client side.
    # Disabling CGO shrinks the binary a little more.
    CGO_ENABLED = "0";
    ldflags = [
      # "-s" "-w"  # only saves 3.6% of image size
      "-X github.com/dnr/styx/common.GzipBin=${gzStaticBin}/bin/gzip"
      "-X github.com/dnr/styx/common.XzBin=${xzStaticBin}/bin/xz"
      "-X github.com/dnr/styx/common.ZstdBin=${zstdStaticBin}/bin/zstd"
      "-X github.com/dnr/styx/common.Version=${base.version}"
    ];
  });

  # Use static binaries and take only the main binaries to make the image as
  # small as possible:
  zstdStaticBin = pkgs.stdenv.mkDerivation {
    name = "zstd-binonly";
    src = pkgs.pkgsStatic.zstd;
    installPhase = "mkdir -p $out/bin && cp $src/bin/zstd $out/bin/";
  };
  xzStaticBin = pkgs.stdenv.mkDerivation {
    name = "xz-binonly";
    src = pkgs.pkgsStatic.xz;
    installPhase = "mkdir -p $out/bin && cp $src/bin/xz $out/bin/";
  };
  gzStaticBin = pkgs.stdenv.mkDerivation {
    name = "gzip-binonly";
    src = pkgs.pkgsStatic.gzip;
    installPhase = "mkdir -p $out/bin && cp $src/bin/.gzip-wrapped $out/bin/gzip";
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
