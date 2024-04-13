{ pkgs ? import <nixpkgs> {} }:
rec {
  src = {
    pname = "styx";
    version = "0.0.6";
    vendorHash = "sha256-gUYQnRInqdPKY8Di9py+/PiE/fzJIChgMVSRbX4B8Ps=";
    src = pkgs.lib.sourceByRegex ./. [
      ".*\.go$"
      "^go\.(mod|sum)$"
      "^(cmd|cmd/styx|common|daemon|erofs|manifester|pb)$"
    ];
    subPackages = [ "cmd/styx" ];
  };

  styx-local = pkgs.buildGoModule (src // {
    # buildInputs = with pkgs; [
    #   brotli.dev
    # ];
    ldflags = with pkgs; [
      "-X common.GzipBin=${gzip}/bin/gzip"
      "-X common.NixBin=${nix}/bin/nix"
      "-X common.XzBin=${xz}/bin/xz"
      "-X common.ZstdBin=${zstd}/bin/zstd"
      "-X common.Version=${src.version}"
    ];
  });

  styx-image = pkgs.buildGoModule (src // {
    tags = [ "lambda.norpc" ];
    # CGO is only needed for cbrotli, which is only used on the client side.
    # Disabling CGO shrinks the binary a little more.
    CGO_ENABLED = "0";
    ldflags = [
      # "-s" "-w"  # only saves 3.6% of image size
      "-X common.GzipBin=${gzStaticBin}/bin/gzip"
      "-X common.XzBin=${xzStaticBin}/bin/xz"
      "-X common.ZstdBin=${zstdStaticBin}/bin/zstd"
      "-X common.Version=${src.version}"
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
      Entrypoint = [ "${styx-image}/bin/styx" ];
    };
  };
}
