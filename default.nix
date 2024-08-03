{ pkgs ? import <nixpkgs> { config = {}; overlays = []; } }:
rec {
  base = {
    pname = "styx";
    version = "0.0.6";
    vendorHash = "sha256-0FPPnYLxG3KNKSXWxdcUOMEEzQ02W7NLa009EmDVaq4=";
    src = pkgs.lib.sourceByRegex ./. [
      "^go\\.(mod|sum)$"
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
      go test -ldflags="$ldflags" -c -o styxtest ./tests
    '';
    installPhase = ''
      mkdir -p $out/bin $out/keys
      install styxtest $out/bin/
      cp keys/testsuite* $out/keys/
    '';
    ldflags = daemonLdFlags ++ [
      "-X github.com/dnr/styx/tests.TestdataDir=${testdata}"
    ];
  });

  testdata = let
    paths = [
      "cd1nbildgzzfryjg82njnn36i4ynyf8h-bash-interactive-5.1-p16-man"
      "8vyj9c6g424mz0v3kvzkskhvzhwj6288-bash-interactive-5.2-p15-man"
      "3a7xq2qhxw2r7naqmc53akmx7yvz0mkf-less-is-more.patch"
      "v35ysx9k1ln4c6r7lj74204ss4bw7l5l-openssl-3.0.12-man"
      "xd96wmj058ky40aywv72z63vdw9yzzzb-openssl-3.0.12-man"
      "1fka6ngkrlmqkhix0gnnb19z58sr0yma-openssl-3.0.13-man"
      "z2waz77lsh4pxs0jxgmpf16s7a3g7b7v-openssl-3.0.13-man"
      "53qwclnym7a6vzs937jjmsfqxlxlsf2y-opusfile-0.12"
      "kcyrz2y8si9ry5p8qkmj0gp41n01sa1y-opusfile-0.12"
      "qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12"
    ];
    hash = "sha256-NcLWG05a8YLXM62cSQR6D/Ne9/YbS/NkPspzdF7u648=";
  in pkgs.stdenv.mkDerivation {
    name = "styx-test-data";
    builder = pkgs.writeShellScript "build-testdata" ''
      PATH=${pkgs.coreutils}/bin:${pkgs.gnused}/bin:${pkgs.wget}/bin:$PATH
      mkdir $out && cd $out
      ${pkgs.lib.concatMapStringsSep "\n" (p: ''
        p=${p}; ni=''${p%%-*}.narinfo
        wget -nv -x -nH https://cache.nixos.org/$ni
        wget -nv -x -nH https://cache.nixos.org/$(sed -ne '/URL/s/.* //p' $ni)
      '') paths}
    '';
    outputHashMode = "recursive";
    outputHash = hash;
  };

  # built by deploy-ci, for heavy CI worker on EC2
  charon = pkgs.buildGoModule (base // {
    pname = "charon";
    subPackages = [ "cmd/charon" ];
  });
  # for light CI worker on non-AWS server (dd5):
  charon-light = pkgs.buildGoModule (base // {
    pname = "charon";
    subPackages = [ "cmd/charon" ];
    ldflags = [ ];
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
      Entrypoint = [ "${charon-light}/bin/charon" ];
    };
  };
}
